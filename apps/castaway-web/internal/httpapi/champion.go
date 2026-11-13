package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/draftparse"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/gameplay"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Pick Your Champion: from the merge each player picks one castaway still in the game. Picks can change until
// the round locks, then they're fixed for the season and visible to everyone. Challenges after the lock award
// the winners' pickers; a voted-out Champion can't win, so it stops scoring.

type championRound struct {
	MergeEpisode int       `json:"merge_episode"`
	OpensAt      time.Time `json:"opens_at"`
	LocksAt      time.Time `json:"locks_at"`
}

type championPick struct {
	Player     string `json:"player"`
	Contestant string `json:"contestant"`
}

// putChampionRound creates or reschedules the instance's round; once it has locked it can't change.
func (s *Server) putChampionRound(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok || !s.requireInstanceAdminRequest(c, instanceID) {
		return
	}
	var req championRound
	if err := c.ShouldBindJSON(&req); err != nil || req.MergeEpisode <= 0 || !req.LocksAt.After(req.OpensAt) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "merge_episode, opens_at and a later locks_at are required"})
		return
	}
	tag, err := s.pool.Exec(c.Request.Context(), `INSERT INTO champion_rounds (instance_id, merge_episode, opens_at, locks_at)
		SELECT id, $2, $3, $4 FROM instances WHERE public_id = $1
		ON CONFLICT (instance_id) DO UPDATE SET merge_episode = EXCLUDED.merge_episode, opens_at = EXCLUDED.opens_at, locks_at = EXCLUDED.locks_at
		WHERE champion_rounds.locks_at > $5`, toPGUUID(instanceID), req.MergeEpisode, req.OpensAt.UTC(), req.LocksAt.UTC(), s.now())
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, errorResponse{Error: "Champion picks already locked"})
		return
	}
	c.JSON(http.StatusOK, req)
}

func (s *Server) loadChampionRound(ctx context.Context, instanceID uuid.UUID) (*championRound, error) {
	var r championRound
	err := s.pool.QueryRow(ctx, `SELECT r.merge_episode, r.opens_at, r.locks_at FROM champion_rounds r JOIN instances i ON i.id = r.instance_id
		WHERE i.public_id = $1`, toPGUUID(instanceID)).Scan(&r.MergeEpisode, &r.OpensAt, &r.LocksAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// championPicks lists everyone's picks (callers show them only after the lock) and the viewer's own.
func (s *Server) championPicks(ctx context.Context, instanceID uuid.UUID, discordUserID string) (all []championPick, mine string, err error) {
	rows, err := s.pool.Query(ctx, `SELECT p.name, c.name, COALESCE(p.discord_user_id = $2, false) FROM champion_picks cp
		JOIN instances i ON i.id = cp.instance_id JOIN participants p ON p.id = cp.participant_id JOIN contestants c ON c.id = cp.contestant_id
		WHERE i.public_id = $1 ORDER BY lower(p.name)`, toPGUUID(instanceID), discordUserID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var p championPick
		var own bool
		if err := rows.Scan(&p.Player, &p.Contestant, &own); err != nil {
			return nil, "", err
		}
		all = append(all, p)
		if own {
			mine = p.Contestant
		}
	}
	return all, mine, rows.Err()
}

// activeContestants are the instance's castaways without a finishing place yet.
func (s *Server) activeContestants(ctx context.Context, instanceID uuid.UUID) ([]draftparse.Contestant, error) {
	rows, err := s.pool.Query(ctx, `SELECT c.public_id::text, c.name FROM instance_contestants ic JOIN instances i ON i.id = ic.instance_id
		JOIN contestants c ON c.id = ic.contestant_id
		WHERE i.public_id = $1 AND NOT EXISTS (SELECT 1 FROM outcome_positions o WHERE o.instance_id = ic.instance_id AND o.contestant_id = ic.contestant_id)
		ORDER BY c.name`, toPGUUID(instanceID))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (draftparse.Contestant, error) {
		var c draftparse.Contestant
		err := r.Scan(&c.ID, &c.Name)
		return c, err
	})
}

// saveChampionPick sets a linked player's pick from free text (website choice or Discord message).
func (s *Server) saveChampionPick(ctx context.Context, instanceID uuid.UUID, discordUserID, text string) (string, error) {
	round, err := s.loadChampionRound(ctx, instanceID)
	if err != nil {
		return "", err
	}
	now := s.now()
	switch {
	case round == nil || now.Before(round.OpensAt):
		return "", progressionError(http.StatusConflict, "Champion picks aren't open yet")
	case !now.Before(round.LocksAt):
		return "", progressionError(http.StatusConflict, "Champion picks are locked")
	}
	active, err := s.activeContestants(ctx, instanceID)
	if err != nil {
		return "", err
	}
	match, hint := draftparse.MatchOne(text, draftparse.NewRoster(active))
	if match == nil {
		msg := fmt.Sprintf("%q isn't a castaway still in the game", strings.TrimSpace(text))
		if hint != "" {
			msg += " (" + hint + ")"
		}
		return "", progressionError(http.StatusBadRequest, msg)
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO champion_picks (instance_id, participant_id, contestant_id, picked_at)
		SELECT i.id, p.id, c.id, $4 FROM instances i JOIN participants p ON p.instance_id = i.id JOIN contestants c ON c.public_id = $3
		WHERE i.public_id = $1 AND p.discord_user_id = $2
		ON CONFLICT (instance_id, participant_id) DO UPDATE SET contestant_id = EXCLUDED.contestant_id, picked_at = EXCLUDED.picked_at`,
		toPGUUID(instanceID), discordUserID, match.ID, now)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", progressionError(http.StatusForbidden, "only linked players in this season can pick a Champion")
	}
	return match.Name, nil
}

// putChampionPickAPI is the bot's path: a player's reply to the merge post, in a channel bound to the season.
// The trusted service names the player by Discord ID.
func (s *Server) putChampionPickAPI(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	var req struct {
		DiscordUserID string `json:"discord_user_id" binding:"required"`
		Contestant    string `json:"contestant" binding:"required"`
	}
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "discord_user_id and contestant are required"})
		return
	}
	var instanceID pgtype.UUID
	err := s.pool.QueryRow(c.Request.Context(), `SELECT i.public_id FROM discord_channel_bindings b JOIN instances i ON i.id = b.instance_id
		WHERE b.guild_id = $1 AND b.channel_id = $2`, c.Param("guildID"), c.Param("channelID")).Scan(&instanceID)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	name, err := s.saveChampionPick(c.Request.Context(), uuid.UUID(instanceID.Bytes), req.DiscordUserID, req.Contestant)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"contestant": name})
}

// getChampionRound shows the round and, once locked, everyone's picks.
func (s *Server) getChampionRound(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok || !s.requireInstanceAdminRequest(c, instanceID) {
		return
	}
	round, err := s.loadChampionRound(c.Request.Context(), instanceID)
	if err == nil && round == nil {
		c.JSON(http.StatusNotFound, errorResponse{Error: "no Champion round"})
		return
	}
	var picks []championPick
	if err == nil && !s.now().Before(round.LocksAt) {
		picks, _, err = s.championPicks(c.Request.Context(), instanceID, "")
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"round": round, "picks": picks})
}

// applyChampionChallenge records castaways winning a challenge and awards the players whose Champion (locked
// before the challenge) won. Replaying the same key with the same winners is a no-op.
func applyChampionChallenge(ctx context.Context, tx pgx.Tx, qtx *db.Queries, instanceID uuid.UUID, key, kind string, winners []string, at time.Time) (int, error) {
	if _, ok := gameplay.ChampionChallengePoints[kind]; !ok || len(winners) == 0 {
		return 0, progressionError(http.StatusBadRequest, "champion challenges need kind immunity or reward and winners")
	}
	winners = slices.Clone(winners)
	sort.Strings(winners)
	winners = slices.Compact(winners)
	activities, err := qtx.ListInstanceActivitiesByType(ctx, db.ListInstanceActivitiesByTypeParams{InstanceID: toPGUUID(instanceID), ActivityType: gameplay.ChampionChallengeActivityType})
	if err != nil {
		return 0, err
	}
	var activityID pgtype.UUID
	if len(activities) > 0 {
		activityID = activities[0].ID
		existing, err := qtx.GetActivityOccurrenceBySourceRef(ctx, db.GetActivityOccurrenceBySourceRefParams{ActivityID: activityID, SourceRef: pgtype.Text{String: key, Valid: true}})
		if err == nil {
			var stored map[string][]string
			if err := json.Unmarshal(existing.Metadata, &stored); err != nil {
				return 0, err
			}
			if existing.OccurrenceType != kind || !slices.Equal(stored["winning_contestant_ids"], winners) {
				return 0, progressionError(http.StatusConflict, "key "+key+" already recorded with a different result")
			}
			entries, err := qtx.ListVisibleBonusPointLedgerEntriesByOccurrence(ctx, existing.ID)
			return len(entries), err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
	} else {
		activity, err := qtx.CreateInstanceActivity(ctx, db.CreateInstanceActivityParams{InstanceID: toPGUUID(instanceID), ActivityType: gameplay.ChampionChallengeActivityType, Name: "Champion challenges", Status: "active", StartsAt: wordleTimestamp(at), Metadata: []byte(`{}`)})
		if err != nil {
			return 0, err
		}
		activityID = activity.ID
	}
	type picker struct {
		participant       pgtype.UUID
		contestant, cname string
	}
	rows, err := tx.Query(ctx, `SELECT p.public_id, c.public_id::text, c.name FROM champion_picks cp
		JOIN champion_rounds r ON r.instance_id = cp.instance_id JOIN instances i ON i.id = cp.instance_id
		JOIN participants p ON p.id = cp.participant_id JOIN contestants c ON c.id = cp.contestant_id
		WHERE i.public_id = $1 AND r.locks_at <= $2 AND c.public_id::text = ANY($3::text[])`, toPGUUID(instanceID), at, winners)
	if err != nil {
		return 0, err
	}
	pickers, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (picker, error) {
		var p picker
		err := r.Scan(&p.participant, &p.contestant, &p.cname)
		return p, err
	})
	if err != nil {
		return 0, err
	}
	metadata, err := json.Marshal(map[string][]string{"winning_contestant_ids": winners})
	if err != nil {
		return 0, err
	}
	occurrence, err := qtx.CreateActivityOccurrence(ctx, db.CreateActivityOccurrenceParams{
		ActivityID: activityID, OccurrenceType: kind, Name: "Champion " + kind, EffectiveAt: wordleTimestamp(at),
		Status: "recorded", SourceRef: pgtype.Text{String: key, Valid: true}, Metadata: metadata,
	})
	if err != nil {
		return 0, err
	}
	for _, p := range pickers {
		meta, err := json.Marshal(map[string]string{"contestant_id": p.contestant, "contestant_name": p.cname})
		if err != nil {
			return 0, err
		}
		if _, err := qtx.CreateActivityOccurrenceParticipant(ctx, db.CreateActivityOccurrenceParticipantParams{
			ActivityOccurrenceID: occurrence.ID, ParticipantID: p.participant, Role: "champion_picker", Result: "won", Metadata: meta,
		}); err != nil {
			return 0, err
		}
	}
	resolved, err := gameplay.NewService(qtx).ResolveActivityOccurrence(ctx, occurrence.ID)
	return len(resolved), err
}

type championView struct {
	Open, Locked bool
	MergeEpisode int
	LocksAt      time.Time
	Mine         string
	MineOut      bool // before the lock: their pick has been voted out
	Options      []string
	Picks        []championPick // everyone's, after the lock
	Message      string
}

// addChampionGame lists Pick Your Champion under its merge episode on /games.
func (s *Server) addChampionGame(ctx context.Context, data *sitePageData, instanceID uuid.UUID) error {
	round, err := s.loadChampionRound(ctx, instanceID)
	if err != nil || round == nil {
		return err
	}
	ep := int32(round.MergeEpisode) // #nosec G115 -- small episode number
	data.addGame(&ep, gameEntry{Kind: "Pick Your Champion", Name: "Pick Your Champion", URL: "/games/champion", Badge: "Rest of season",
		Status: gameStatus(s.now(), round.OpensAt, round.LocksAt, true)})
	return nil
}

func (s *Server) championPage(c *gin.Context) {
	s.renderChampion(c, "")
}

func (s *Server) pickChampion(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed {
		c.Status(http.StatusForbidden)
		return
	}
	id, err := uuid.Parse(s.public.InstanceID)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	name, err := s.saveChampionPick(c.Request.Context(), id, data.User.DiscordUserID, c.PostForm("contestant"))
	var failure *progressionFailure
	switch {
	case errors.As(err, &failure):
		c.Status(failure.status)
		s.renderChampion(c, failure.message)
	case err != nil:
		c.Status(http.StatusInternalServerError)
	default:
		s.renderChampion(c, "Your Champion is "+name+".")
	}
}

func (s *Server) renderChampion(c *gin.Context, message string) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed {
		renderSite(c, "home.html", data)
		return
	}
	ctx := c.Request.Context()
	id, err := uuid.Parse(s.public.InstanceID)
	var round *championRound
	if err == nil {
		round, err = s.loadChampionRound(ctx, id)
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	if round == nil {
		c.Redirect(http.StatusFound, "/games")
		return
	}
	view := &championView{MergeEpisode: round.MergeEpisode, LocksAt: easternTime(round.LocksAt), Message: message}
	now := s.now()
	view.Locked = !now.Before(round.LocksAt)
	view.Open = !view.Locked && !now.Before(round.OpensAt)
	var all []championPick
	if err == nil {
		all, view.Mine, err = s.championPicks(ctx, id, data.User.DiscordUserID)
	}
	if view.Locked {
		view.Picks = all
	} else if err == nil {
		var active []draftparse.Contestant
		active, err = s.activeContestants(ctx, id)
		for _, a := range active {
			view.Options = append(view.Options, a.Name)
		}
		view.MineOut = view.Mine != "" && !slices.Contains(view.Options, view.Mine)
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	data.Champion = view
	renderSite(c, "champion.html", data)
}
