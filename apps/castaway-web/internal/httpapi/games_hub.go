package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Every game lives under /games: /games lists a season's games by episode, and /games/<kind>/<id> plays a
// game while it's open and shows everyone's results once it's over. Results are visible to the season's
// players; admin-only test games never show up outside the admin's own list.

type gameEntry struct {
	Kind, Name, URL, Badge, Status string
}

type gameEpisode struct {
	Number int32
	Games  []gameEntry
}

func gameStatus(now, opens, cutoff time.Time, scored bool) string {
	switch {
	case now.Before(opens):
		return "Upcoming"
	case now.Before(cutoff):
		return "Open"
	case scored:
		return "Results"
	default:
		return "Closed"
	}
}

// addGame files an entry under its episode (episodes newest first) or, without one, under tests.
func (data *sitePageData) addGame(episode *int32, entry gameEntry) {
	if episode == nil {
		data.TestGames = append(data.TestGames, entry)
		return
	}
	for i := range data.GameEpisodes {
		if data.GameEpisodes[i].Number == *episode {
			data.GameEpisodes[i].Games = append(data.GameEpisodes[i].Games, entry)
			return
		}
	}
	data.GameEpisodes = append(data.GameEpisodes, gameEpisode{Number: *episode, Games: []gameEntry{entry}})
	sort.Slice(data.GameEpisodes, func(i, j int) bool { return data.GameEpisodes[i].Number > data.GameEpisodes[j].Number })
}

// addButtonGames lists an instance's Press the Button games (tests only for admins).
func (s *Server) addButtonGames(ctx context.Context, data *sitePageData, instanceID string) error {
	rows, err := s.pool.Query(ctx, `SELECT g.public_id::text, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM button_games g JOIN instances i ON i.id = g.instance_id WHERE i.public_id::text = $1 AND (g.episode_number IS NOT NULL OR $2)
		ORDER BY g.opens_at`, instanceID, data.Admin)
	if err != nil {
		return err
	}
	defer rows.Close()
	now := s.now()
	for rows.Next() {
		var id string
		var episode *int32
		var opens, cutoff time.Time
		var resolved bool
		if err := rows.Scan(&id, &episode, &opens, &cutoff, &resolved); err != nil {
			return err
		}
		badge := "Scored"
		if episode == nil {
			badge, resolved = "Admin-only test", true
		}
		data.addGame(episode, gameEntry{Kind: "Press the Button", Name: "Press the Button", URL: "/games/button/" + id, Badge: badge, Status: gameStatus(now, opens, cutoff, resolved)})
	}
	return rows.Err()
}

func (s *Server) castawordleEntries(data *sitePageData) {
	now := s.now()
	for _, g := range data.Games {
		badge := "Scored"
		switch {
		case g.Private:
			badge = "Just for you · Scored"
		case g.Test:
			badge = "Admin-only test"
		case g.Unscored:
			badge = "Unscored"
		}
		data.addGame(g.EpisodeNumber, gameEntry{Kind: "Castawordle", Name: g.Name, URL: "/games/castawordle/" + g.ID, Badge: badge,
			Status: gameStatus(now, g.OpensAt, g.CutoffAt, true)})
	}
}

// legacyGameRedirect sends old /castawordle and /button links to their /games pages.
func legacyGameRedirect(c *gin.Context) {
	target := "/games"
	switch c.FullPath() {
	case "/castawordle/:gameID":
		target = "/games/castawordle/" + c.Param("gameID")
	case "/button/:gameID":
		target = "/games/button/" + c.Param("gameID")
	case "/button":
		target = "/games/button"
	}
	c.Redirect(http.StatusMovedPermanently, target)
}

type castawordleResult struct {
	ID, Name string
	Guesses  int
	Solved   bool
}

// castawordleResults is everyone's result for a closed game, private stand-ins included, best first.
func (s *Server) castawordleResults(ctx context.Context, gameID pgtype.UUID, answer string) ([]castawordleResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			requestLogger.Error("rollback castawordle results", "error", err)
		}
	}()
	plays, err := s.queries.WithTx(tx).ListCastawordlePlays(ctx, gameID)
	if err != nil {
		return nil, err
	}
	plays, _, err = withPrivateCastawordlePlays(ctx, tx, gameID, answer, plays)
	if err != nil {
		return nil, err
	}
	var results []castawordleResult
	for _, play := range plays {
		var guesses []string
		if err := json.Unmarshal(play.Guesses, &guesses); err != nil {
			return nil, err
		}
		r := castawordleResult{ID: pgUUIDString(play.ParticipantID), Guesses: len(guesses), Solved: play.Status == "solved"}
		if err := tx.QueryRow(ctx, `SELECT name FROM participants WHERE public_id = $1`, play.ParticipantID).Scan(&r.Name); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.Solved != b.Solved {
			return a.Solved
		}
		if a.Guesses != b.Guesses {
			return a.Solved == (a.Guesses < b.Guesses)
		}
		return a.Name < b.Name
	})
	return results, nil
}

type buttonView struct {
	ID          string
	Episode     *int32
	Test        bool
	Open        bool
	Upcoming    bool
	OpensAt     time.Time
	CutoffAt    time.Time
	Action      string
	Count       int64
	Tribes      []buttonTribeTotal
	Results     []buttonResult // set once results are visible
	ResultsWait bool           // closed, waiting to be scored
}

type buttonResult struct {
	Rank  int
	ID    string
	Name  string
	Tribe string
	buttonScore
}

var errButtonNotFound = errors.New("game not found")

// loadButtonView loads a game for the signed-in player: the open scheduled game for /games/button, or
// /games/button/:gameID (tests are admin-only).
func (s *Server) loadButtonView(c *gin.Context, data sitePageData) (*buttonView, int64, error) {
	ctx := c.Request.Context()
	var dbID int64
	view := buttonView{}
	var resolved bool
	query := `SELECT g.id, g.public_id::text, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM button_games g JOIN instances i ON i.id = g.instance_id WHERE i.public_id = $1 AND `
	args := []any{s.public.InstanceID}
	if id := c.Param("gameID"); id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return nil, 0, errButtonNotFound
		}
		query += `g.public_id::text = $2 AND (g.episode_number IS NOT NULL OR $3)`
		args = append(args, id, data.Admin)
	} else {
		query += `g.episode_number IS NOT NULL AND g.opens_at <= $2 AND g.cutoff_at > $2 ORDER BY g.opens_at DESC LIMIT 1`
		args = append(args, s.now())
	}
	err := s.pool.QueryRow(ctx, query, args...).Scan(&dbID, &view.ID, &view.Episode, &view.OpensAt, &view.CutoffAt, &resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, errButtonNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	now := s.now()
	view.Test = view.Episode == nil
	view.Open = !now.Before(view.OpensAt) && now.Before(view.CutoffAt)
	view.Upcoming = now.Before(view.OpensAt)
	view.OpensAt, view.CutoffAt = easternTime(view.OpensAt), easternTime(view.CutoffAt)
	view.Action = "/games/button/" + view.ID
	return &view, dbID, nil
}

// fillButtonView adds the player's count and tribe totals (open), or everyone's results (once scored).
func (s *Server) fillButtonView(ctx context.Context, view *buttonView, dbID int64, discordID string, resolved bool) error {
	at := s.now()
	if at.After(view.CutoffAt) {
		at = view.CutoffAt
	}
	players, err := loadButtonPlayers(ctx, s.pool, dbID, at)
	if err != nil {
		return err
	}
	if view.Open {
		view.Tribes = tribePresses(players)
		view.Count = s.buttonPresses(ctx, dbID, discordID)
		return nil
	}
	if view.Upcoming {
		return nil
	}
	if !resolved && !view.Test {
		view.ResultsWait = true
		return nil
	}
	view.Tribes = tribePresses(players)
	scores := buttonScores(players)
	for _, p := range players {
		view.Results = append(view.Results, buttonResult{ID: p.ID, Name: p.Name, Tribe: p.Tribe, buttonScore: scores[p.ID]})
	}
	sort.SliceStable(view.Results, func(i, j int) bool { return view.Results[i].Total > view.Results[j].Total })
	for i := range view.Results {
		view.Results[i].Rank = i + 1
		if i > 0 && view.Results[i].Total == view.Results[i-1].Total {
			view.Results[i].Rank = view.Results[i-1].Rank
		}
	}
	return nil
}

type profileGame struct {
	Episode int32
	Kind    string
	URL     string
	Result  string
}

// profileGames is a player's finished, season-visible game results for one season, newest episode first.
func (s *Server) profileGames(ctx context.Context, participantID string) ([]profileGame, error) {
	var games []profileGame
	now := s.now()
	rows, err := s.pool.Query(ctx, `SELECT COALESCE(g.episode_number, o.episode_number), COALESCE(o.public_id, g.public_id)::text, cp.guesses, cp.status
		FROM castawordle_plays cp JOIN castawordle_games g ON g.id = cp.game_id JOIN participants p ON p.id = cp.participant_id
		LEFT JOIN castawordle_games o ON o.id = g.replaces_game_id
		WHERE p.public_id = $1 AND COALESCE(g.episode_number, o.episode_number) IS NOT NULL AND g.cutoff_at <= $2`, participantID, now)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g profileGame
		var id, status string
		var guesses []string
		if err := rows.Scan(&g.Episode, &id, &guesses, &status); err != nil {
			rows.Close()
			return nil, err
		}
		g.Kind, g.URL = "Castawordle", "/games/castawordle/"+id
		switch status {
		case "solved":
			g.Result = fmt.Sprintf("Solved in %d", len(guesses))
		case "exhausted":
			g.Result = "Torch out"
		default:
			g.Result = fmt.Sprintf("Unfinished (%d guesses)", len(guesses))
		}
		games = append(games, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT g.id, g.public_id::text, g.episode_number FROM button_games g JOIN button_presses b ON b.game_id = g.id
		JOIN participants p ON p.id = b.participant_id WHERE p.public_id = $1 AND g.episode_number IS NOT NULL AND g.resolved_at IS NOT NULL`, participantID)
	if err != nil {
		return nil, err
	}
	type buttonRef struct {
		dbID    int64
		id      string
		episode int32
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (buttonRef, error) {
		var r buttonRef
		return r, row.Scan(&r.dbID, &r.id, &r.episode)
	})
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		var cutoff time.Time
		if err := s.pool.QueryRow(ctx, `SELECT cutoff_at FROM button_games WHERE id = $1`, r.dbID).Scan(&cutoff); err != nil {
			return nil, err
		}
		players, err := loadButtonPlayers(ctx, s.pool, r.dbID, cutoff)
		if err != nil {
			return nil, err
		}
		score := buttonScores(players)[participantID]
		games = append(games, profileGame{Episode: r.episode, Kind: "Press the Button", URL: "/games/button/" + r.id,
			Result: fmt.Sprintf("%d presses · %+d pts", score.Presses, score.Total)})
	}
	scramble, err := s.scrambleProfileGames(ctx, participantID)
	if err != nil {
		return nil, err
	}
	games = append(games, scramble...)
	track, err := s.trackProfileGames(ctx, participantID)
	if err != nil {
		return nil, err
	}
	games = append(games, track...)
	freeTheBlock, err := s.freeTheBlockProfileGames(ctx, participantID)
	if err != nil {
		return nil, err
	}
	games = append(games, freeTheBlock...)
	sort.SliceStable(games, func(i, j int) bool {
		if games[i].Episode != games[j].Episode {
			return games[i].Episode > games[j].Episode
		}
		return games[i].Kind < games[j].Kind
	})
	return games, nil
}
