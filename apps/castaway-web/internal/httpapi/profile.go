package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A profile is one player's seasons in this league, joined by Discord account. Each page shows one season
// (score + draft) and lists the player's other league seasons.
type profileSeason struct {
	ParticipantID, InstanceID, InstanceName  string
	Season                                   int32
	Current, Selected                        bool
	Scored                                   bool // has a draft on the leaderboard; otherwise "Unavailable"
	Rank, Players, Total, DraftPoints, Bonus int
	Tribe                                    string
}

type profilePick struct {
	Position   int32
	Name       string
	Scored     bool // this castaway's finishing position has been scored
	Value      int  // finishing value: last place 1 ... winner N
	Distance   int  // |drafted position - finishing position|
	Eliminated bool // scored and the season is still running (shown italic)
	Points     int  // max(0, Value - Distance)
}

type profileView struct {
	PlayerName, LeagueName string
	Self                   bool
	Pronouns               string // set only when Self: players see their own pronouns, nobody else's
	Season                 profileSeason
	Draft                  []profilePick
	DraftHidden            string
	Seasons                []profileSeason
	Games                  []profileGame
}

type leaderboardRow struct {
	ParticipantID string `json:"participant_id"`
	Name          string `json:"participant_name"`
	Tribe         string `json:"current_tribe_name"`
	Draft         int    `json:"draft_points"`
	Bonus         int    `json:"bonus_points"`
	Total         int    `json:"total_points"`
	HasDraft      *bool  `json:"has_draft"`
	Rank          int    `json:"-"`
}

// leaderboardRows runs the leaderboard handler in-process (visible bonus only; secret points never count)
// and returns players with drafts, ranked by total with ties sharing a rank.
// ponytail: one leaderboard computation per season shown; cache or extract if profiles get slow.
func (s *Server) leaderboardRows(ctx context.Context, instanceID string) ([]leaderboardRow, error) {
	rec := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(rec)
	gc.Request = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	gc.Params = gin.Params{{Key: "instanceID", Value: instanceID}}
	s.leaderboard(gc)
	if rec.Code != http.StatusOK {
		return nil, fmt.Errorf("leaderboard HTTP %d", rec.Code)
	}
	var res struct {
		Leaderboard []leaderboardRow `json:"leaderboard"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		return nil, err
	}
	rows := res.Leaderboard[:0]
	for _, r := range res.Leaderboard {
		if r.HasDraft == nil || *r.HasDraft {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Total > rows[j].Total })
	for i := range rows {
		rows[i].Rank = i + 1
		if i > 0 && rows[i].Total == rows[i-1].Total {
			rows[i].Rank = rows[i-1].Rank
		}
	}
	return rows, nil
}

func (s *Server) leagueInstanceIDs() []string {
	return append([]string{s.public.InstanceID}, s.public.LeagueInstanceIDs...)
}

func (s *Server) mePage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	var participantID string
	err := s.pool.QueryRow(c.Request.Context(), `SELECT p.public_id::text FROM participants p JOIN instances i ON i.id = p.instance_id
		WHERE i.public_id = $1 AND p.discord_user_id = $2`, s.public.InstanceID, data.User.DiscordUserID).Scan(&participantID)
	if !data.Allowed || errors.Is(err, pgx.ErrNoRows) {
		c.Redirect(http.StatusFound, "/") // not a player this season: Scores (or the access request) instead
		return
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	s.renderProfile(c, data, participantID)
}

func (s *Server) playerPage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed && !data.Viewer {
		c.Redirect(http.StatusFound, "/")
		return
	}
	id, err := uuid.Parse(c.Param("participantID"))
	if err != nil {
		c.String(http.StatusNotFound, "player not found")
		return
	}
	s.renderProfile(c, data, id.String())
}

func (s *Server) renderProfile(c *gin.Context, data sitePageData, participantID string) {
	ctx := c.Request.Context()
	league := s.leagueInstanceIDs()
	var targetInstance, discordID string
	var name string
	// Any league player is viewable (by signed-in current-season players); other instances are not.
	err := s.pool.QueryRow(ctx, `SELECT p.name, i.public_id::text, COALESCE(p.discord_user_id, '') FROM participants p JOIN instances i ON i.id = p.instance_id
		WHERE p.public_id = $1 AND i.public_id::text = ANY($2)`,
		participantID, league).Scan(&name, &targetInstance, &discordID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.String(http.StatusNotFound, "player not found")
		return
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	view := profileView{PlayerName: name, LeagueName: s.public.LeagueName, Self: discordID != "" && discordID == data.User.DiscordUserID}
	if view.Self {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(pronouns, '') FROM participants WHERE public_id = $1`, participantID).Scan(&view.Pronouns); err != nil {
			c.String(http.StatusInternalServerError, "could not load the page")
			return
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT p.public_id::text, i.public_id::text, i.name, i.season FROM participants p JOIN instances i ON i.id = p.instance_id
		WHERE i.public_id::text = ANY($1) AND (p.public_id = $2 OR ($3 <> '' AND p.discord_user_id = $3))
		ORDER BY i.season DESC, i.created_at DESC`, league, participantID, discordID)
	if err == nil {
		view.Seasons, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (profileSeason, error) {
			var season profileSeason
			err := row.Scan(&season.ParticipantID, &season.InstanceID, &season.InstanceName, &season.Season)
			return season, err
		})
	}
	for i := range view.Seasons {
		if err != nil {
			break
		}
		season := &view.Seasons[i]
		season.Current = season.InstanceID == s.public.InstanceID
		season.Selected = season.ParticipantID == participantID
		var board []leaderboardRow
		board, err = s.leaderboardRows(ctx, season.InstanceID)
		season.Players = len(board)
		for _, r := range board {
			if r.ParticipantID == season.ParticipantID {
				season.Scored, season.Rank, season.Total, season.DraftPoints, season.Bonus, season.Tribe = true, r.Rank, r.Total, r.Draft, r.Bonus, r.Tribe
			}
		}
		if season.Selected {
			view.Season = *season
		}
	}
	if err == nil {
		view.Draft, view.DraftHidden, err = s.profileDraft(ctx, data, view, targetInstance, participantID)
	}
	if err == nil {
		view.Games, err = s.profileGames(ctx, participantID)
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	data.Profile = &view
	renderSite(c, "profile.html", data)
}

// profileDraft returns the selected season's draft, or why it's hidden. Drafts are soft-closed (late drafts
// are allowed), so another player's current-season draft is shown only once submissions have closed and the
// viewer has saved their own. Past seasons are open; admins and the owner always see it.
func (s *Server) profileDraft(ctx context.Context, data sitePageData, view profileView, instanceID, participantID string) ([]profilePick, string, error) {
	if instanceID == s.public.InstanceID && !view.Self && !data.Admin {
		activity, _, err := draftSubmissionActivity(ctx, s.queries, toPGUUID(uuid.MustParse(instanceID)))
		if err != nil {
			return nil, "", err
		}
		if activity != nil && activity.Status == "active" {
			// Once an episode is scored the season is underway, so drafts are locked in even if nobody ran `draft close`.
			var started bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM outcome_positions op JOIN instances i ON i.id = op.instance_id WHERE i.public_id = $1)`, instanceID).Scan(&started); err != nil {
				return nil, "", err
			}
			if !started {
				return nil, "Drafts are revealed after drafts close.", nil
			}
		}
		var drafted bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM draft_picks dp JOIN participants p ON p.id = dp.participant_id
			JOIN instances i ON i.id = p.instance_id WHERE i.public_id = $1 AND p.discord_user_id = $2)`, instanceID, data.User.DiscordUserID).Scan(&drafted); err != nil {
			return nil, "", err
		}
		if !drafted {
			return nil, "Submit your own draft to see everyone else's.", nil
		}
	}
	picks, err := s.queries.ListDraftPicksForParticipant(ctx, toPGUUID(uuid.MustParse(participantID)))
	if err != nil {
		return nil, "", err
	}
	// Short names as on the show; finishing positions exist only once an elimination has been scored, so
	// eliminations appear when scores are entered, not when an episode airs.
	rows, err := s.pool.Query(ctx, `SELECT c.public_id, COALESCE(c.short_name, c.name), op.position
		FROM instance_contestants ic JOIN instances i ON i.id = ic.instance_id JOIN contestants c ON c.id = ic.contestant_id
		LEFT JOIN outcome_positions op ON op.instance_id = ic.instance_id AND op.contestant_id = c.id
		WHERE i.public_id = $1`, instanceID)
	if err != nil {
		return nil, "", err
	}
	type castaway struct {
		name   string
		finish *int32
	}
	byID := map[[16]byte]castaway{}
	for rows.Next() {
		var id uuid.UUID
		var cw castaway
		if err := rows.Scan(&id, &cw.name, &cw.finish); err != nil {
			rows.Close()
			return nil, "", err
		}
		byID[id] = cw
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	total := len(byID)
	running := instanceID == s.public.InstanceID
	draft := make([]profilePick, 0, len(picks))
	for _, pick := range picks {
		cw := byID[pick.ContestantID.Bytes]
		p := profilePick{Position: pick.Position, Name: cw.name}
		if cw.finish != nil {
			// Mirrors scoring.calculateCurrentScore: max(0, value - distance).
			p.Scored, p.Eliminated = true, running
			p.Value = total - int(*cw.finish) + 1
			p.Distance = int(pick.Position - *cw.finish)
			if p.Distance < 0 {
				p.Distance = -p.Distance
			}
			p.Points = max(0, p.Value-p.Distance)
		}
		draft = append(draft, p)
	}
	if len(draft) == 0 {
		return nil, "No draft on record.", nil
	}
	return draft, "", nil
}

type seasonSummary struct {
	ID, Name, Winner string
	Season           int32
	Current          bool
	Players, Points  int
}

// seasonsPage lists the league's seasons, newest first, with each winner.
func (s *Server) seasonsPage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed && !data.Viewer {
		c.Redirect(http.StatusFound, "/")
		return
	}
	ctx := c.Request.Context()
	rows, err := s.pool.Query(ctx, `SELECT public_id::text, name, season FROM instances WHERE public_id::text = ANY($1) ORDER BY season DESC, created_at DESC`, s.leagueInstanceIDs())
	if err == nil {
		data.Seasons, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (seasonSummary, error) {
			var season seasonSummary
			err := row.Scan(&season.ID, &season.Name, &season.Season)
			return season, err
		})
	}
	for i := range data.Seasons {
		if err != nil {
			break
		}
		season := &data.Seasons[i]
		season.Current = season.ID == s.public.InstanceID
		var board []leaderboardRow
		board, err = s.leaderboardRows(ctx, season.ID)
		season.Players = len(board)
		if len(board) > 0 && !season.Current {
			season.Winner, season.Points = board[0].Name, board[0].Total
			for _, r := range board[1:] {
				if r.Rank == 1 {
					season.Winner += " & " + r.Name
				}
			}
		}
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	renderSite(c, "seasons.html", data)
}

// seasonPage shows a league season's scoreboard, like Scores does for the current season.
func (s *Server) seasonPage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed && !data.Viewer {
		c.Redirect(http.StatusFound, "/")
		return
	}
	id, err := uuid.Parse(c.Param("instanceID"))
	if err != nil {
		c.String(http.StatusNotFound, "season not found")
		return
	}
	if id.String() == s.public.InstanceID {
		c.Redirect(http.StatusFound, "/")
		return
	}
	err = s.pool.QueryRow(c.Request.Context(), `SELECT name FROM instances WHERE public_id::text = ANY($1) AND public_id = $2`, s.public.LeagueInstanceIDs, id).Scan(&data.SeasonName)
	data.SeasonID = id.String()
	if errors.Is(err, pgx.ErrNoRows) {
		c.String(http.StatusNotFound, "season not found")
		return
	}
	var board []leaderboardRow
	if err == nil {
		board, err = s.leaderboardRows(c.Request.Context(), id.String())
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	for _, r := range board {
		data.Rows = append(data.Rows, homeRow{Rank: r.Rank, ID: r.ParticipantID, Name: r.Name, Tribe: r.Tribe, Total: r.Total, Draft: r.Draft, Bonus: r.Bonus})
	}
	data.NoBonus, data.NoTribe = true, true
	for _, r := range board {
		data.NoBonus = data.NoBonus && r.Bonus == 0
		data.NoTribe = data.NoTribe && r.Tribe == ""
	}
	renderSite(c, "home.html", data)
}

// setPronouns lets a signed-in player set their own pronouns (or clear them) on every league season they
// played. Pronouns are used only in Probst message copy and shown only to the player themself.
func (s *Server) setPronouns(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	value := c.PostForm("pronouns")
	if !data.Allowed || (value != "" && value != "he/him" && value != "she/her" && value != "they/them") {
		c.String(http.StatusBadRequest, "invalid pronouns")
		return
	}
	if _, err := s.pool.Exec(c.Request.Context(), `UPDATE participants p SET pronouns = NULLIF($1, '') FROM instances i
		WHERE i.id = p.instance_id AND p.discord_user_id = $2 AND i.public_id::text = ANY($3)`,
		value, data.User.DiscordUserID, s.leagueInstanceIDs()); err != nil {
		c.String(http.StatusInternalServerError, "could not save")
		return
	}
	c.Redirect(http.StatusSeeOther, "/me")
}
