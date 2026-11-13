package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Stack 'Em Up: tilt the phone to balance a tray on a stand while stacking eight letter blocks until the tower
// spells SURVIVOR, then holds it for three seconds (site/assets/stack-em-up.js). One timed run per player per game.
// The page saves the tower's height and the drop count (towers that fell) at "Go", after every block and every
// drop; the save with height 8 means the full tower was held, and finishes the run. The server keeps the clock
// (first save to the finish, so a reload doesn't pause it). The physics runs on the phone (shortcut: a determined
// player could post a fake run; fine for a friends' league, as with Tilt Table).
// League points: every finisher +1, fastest +3, second fastest +2 (ties in tenths share the place, dense).

const (
	stackBlocks   = 8
	stackMaxDrops = 10000 // only bounds a hostile request
	stackMinRun   = 10 * time.Second
)

type stackPlayer struct {
	ID, Name, Tribe string
	Best, Drops     int
	Finished        bool
	Time            time.Duration // finished runs only
}

// stackPlaces ranks finishers by time in tenths (dense, as Tilt Table); players who didn't finish have no place (0).
func stackPlaces(players []stackPlayer) map[string]int {
	tilt := make([]tiltPlayer, len(players))
	for i, p := range players {
		tilt[i] = tiltPlayer{ID: p.ID, Finished: p.Finished, Time: p.Time}
	}
	return tiltPlaces(tilt)
}

// stackPoints gives each player's league points: +1 for finishing and place points.
func stackPoints(players []stackPlayer) (map[string]int, map[string]int) {
	places, points := stackPlaces(players), map[string]int{}
	for _, p := range players {
		points[p.ID] = 0
		if p.Finished {
			points[p.ID] = 1 + tiltPlacePoints(places[p.ID])
		}
	}
	return places, points
}

func loadStackPlayers(ctx context.Context, q scrambleQuerier, gameID int64) ([]stackPlayer, error) {
	rows, err := q.Query(ctx, `SELECT p.public_id::text, p.name, COALESCE(t.name, ''), sp.best_height, sp.drops,
			COALESCE(EXTRACT(EPOCH FROM sp.finished_at - sp.started_at) * 1000, -1)::bigint
		FROM stack_plays sp JOIN participants p ON p.id = sp.participant_id JOIN stack_games g ON g.id = sp.game_id
		LEFT JOIN LATERAL (SELECT pg.name FROM participant_group_membership_periods m JOIN participant_groups pg ON pg.id = m.participant_group_id
			WHERE m.participant_id = p.id AND pg.kind = 'tribe' AND m.starts_at <= g.cutoff_at AND (m.ends_at IS NULL OR m.ends_at > g.cutoff_at)
			ORDER BY m.starts_at DESC LIMIT 1) t ON true
		WHERE sp.game_id = $1 ORDER BY p.name`, gameID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (stackPlayer, error) {
		var p stackPlayer
		var ms int64
		if err := row.Scan(&p.ID, &p.Name, &p.Tribe, &p.Best, &p.Drops, &ms); err != nil {
			return p, err
		}
		p.Finished, p.Time = ms >= 0, time.Duration(ms)*time.Millisecond
		return p, nil
	})
}

// createStackGame creates (or, before anyone plays it, reschedules) an instance's game for an episode.
func (s *Server) createStackGame(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok || !s.requireInstanceAdminRequest(c, instanceID) {
		return
	}
	var req struct {
		Episode  int32     `json:"episode_number" binding:"required"`
		OpensAt  time.Time `json:"opens_at" binding:"required"`
		CutoffAt time.Time `json:"cutoff_at" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !req.CutoffAt.After(req.OpensAt) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "episode_number, opens_at and a later cutoff_at are required"})
		return
	}
	game := tiltGame{Episode: req.Episode}
	err := s.pool.QueryRow(c.Request.Context(), `INSERT INTO stack_games (instance_id, episode_number, opens_at, cutoff_at)
		SELECT id, $2, $3, $4 FROM instances WHERE public_id = $1
		ON CONFLICT (instance_id, episode_number) DO UPDATE SET opens_at = EXCLUDED.opens_at, cutoff_at = EXCLUDED.cutoff_at
			WHERE stack_games.resolved_at IS NULL AND NOT EXISTS (SELECT 1 FROM stack_plays p WHERE p.game_id = stack_games.id)
		RETURNING public_id, opens_at, cutoff_at, resolved_at IS NOT NULL`, instanceID, req.Episode, req.OpensAt, req.CutoffAt).
		Scan(&game.ID, &game.OpensAt, &game.CutoffAt, &game.Resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusConflict, errorResponse{Error: "instance not found, or this episode's game already started or resolved"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "could not save game"})
		return
	}
	c.JSON(http.StatusOK, game)
}

func (s *Server) resolveStackGameRequest(c *gin.Context) {
	gameID, ok := parseUUIDPath(c, "gameID")
	if !ok {
		return
	}
	var instanceID uuid.UUID
	err := s.pool.QueryRow(c.Request.Context(), `SELECT i.public_id FROM stack_games g JOIN instances i ON i.id = g.instance_id WHERE g.public_id = $1`, gameID).Scan(&instanceID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "game not found"})
		return
	}
	if err != nil || !s.requireInstanceAdminRequest(c, instanceID) {
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Error: "could not load game"})
		}
		return
	}
	awards, err := s.resolveStackGame(c.Request.Context(), gameID)
	if err != nil {
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"awards": awards})
}

// resolveStackGame writes every player's points to the bonus ledger once; later calls return nil.
func (s *Server) resolveStackGame(ctx context.Context, gameID uuid.UUID) (map[string]int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			requestLogger.Error("rollback stack em up game", "error", err)
		}
	}()
	var (
		id            int64
		instance      uuid.UUID
		episode       int32
		opens, cutoff time.Time
		resolved      bool
	)
	if err := tx.QueryRow(ctx, `SELECT g.id, i.public_id, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM stack_games g JOIN instances i ON i.id = g.instance_id WHERE g.public_id = $1 FOR UPDATE OF g`, gameID).
		Scan(&id, &instance, &episode, &opens, &cutoff, &resolved); err != nil {
		return nil, err
	}
	if resolved {
		return nil, nil
	}
	if s.now().Before(cutoff) {
		return nil, errScrambleOpen
	}
	players, err := loadStackPlayers(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	places, awards := stackPoints(players)
	if len(players) > 0 {
		q := s.queries.WithTx(tx)
		at := pgtype.Timestamptz{Time: cutoff, Valid: true}
		name := fmt.Sprintf("Stack 'Em Up: Episode %d", episode)
		activity, err := q.CreateInstanceActivity(ctx, db.CreateInstanceActivityParams{
			ActivityType: "stack_em_up", Name: name, Status: "completed", Metadata: []byte("{}"),
			StartsAt: pgtype.Timestamptz{Time: opens, Valid: true}, EndsAt: at, InstanceID: toPGUUID(instance),
		})
		if err != nil {
			return nil, err
		}
		occurrence, err := q.CreateActivityOccurrence(ctx, db.CreateActivityOccurrenceParams{
			OccurrenceType: "stack_em_up", Name: name, EffectiveAt: at, StartsAt: activity.StartsAt, EndsAt: at,
			Status: "resolved", Metadata: []byte("{}"), ActivityID: activity.ID,
		})
		if err != nil {
			return nil, err
		}
		for _, p := range players {
			if awards[p.ID] == 0 {
				continue
			}
			metadata, err := json.Marshal(map[string]any{"place": places[p.ID], "points": awards[p.ID], "finished": p.Finished,
				"time_ms": p.Time.Milliseconds(), "drops": p.Drops, "height": p.Best})
			if err != nil {
				return nil, err
			}
			if _, err := q.CreateBonusPointLedgerEntry(ctx, db.CreateBonusPointLedgerEntryParams{
				EntryKind: "award", Points: int32(awards[p.ID]), Visibility: "public", Reason: name, EffectiveAt: at, // #nosec G115 -- points are 0–6
				AwardKey: pgtype.Text{String: "stack_em_up", Valid: true}, Metadata: metadata,
				InstanceID: toPGUUID(instance), ParticipantID: toPGUUID(uuid.MustParse(p.ID)), ActivityOccurrenceID: occurrence.ID,
			}); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE stack_games SET resolved_at = $2 WHERE id = $1`, id, s.now()); err != nil {
		return nil, err
	}
	return awards, tx.Commit(ctx)
}

func (s *Server) resolveDueStackGames(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT public_id FROM stack_games WHERE resolved_at IS NULL AND cutoff_at <= $1`, s.now())
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.resolveStackGame(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

type stackView struct {
	ID          string
	Episode     int32
	Open        bool
	Upcoming    bool
	OpensAt     time.Time
	CutoffAt    time.Time
	Played      bool // has a run
	Finished    bool
	Time        string
	Best        int
	Drops       int
	Results     []stackResult
	ResultsWait bool
}

type stackResult struct {
	Place    int
	ID, Name string
	Tribe    string
	Finished bool
	Time     string
	TimeMS   int64
	Best     int
	Drops    int
	Points   int
	// The breakdown behind Points: place points (fastest +3, second +2); finishing adds +1.
	PlacePoints int
}

type stackPlay struct {
	height, best, drops int
	started             time.Time
	finished            *time.Time
	exists              bool
}

// loadStack finds /games/stack-em-up/:gameID, or the scheduled game that's open now (else the latest one).
func (s *Server) loadStack(c *gin.Context) (*stackView, int64, error) {
	var view stackView
	var dbID int64
	var resolved bool
	query := `SELECT g.id, g.public_id::text, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM stack_games g JOIN instances i ON i.id = g.instance_id WHERE i.public_id = $1 AND `
	args := []any{s.public.InstanceID}
	if id := c.Param("gameID"); id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return nil, 0, errScrambleNotFound
		}
		query += `g.public_id::text = $2`
		args = append(args, id)
	} else {
		query += `g.opens_at <= $2 ORDER BY g.opens_at DESC LIMIT 1`
		args = append(args, s.now())
	}
	err := s.pool.QueryRow(c.Request.Context(), query, args...).Scan(&dbID, &view.ID, &view.Episode, &view.OpensAt, &view.CutoffAt, &resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, errScrambleNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	now := s.now()
	view.Open = !now.Before(view.OpensAt) && now.Before(view.CutoffAt)
	view.Upcoming = now.Before(view.OpensAt)
	view.ResultsWait = !view.Open && !view.Upcoming && !resolved
	view.OpensAt, view.CutoffAt = easternTime(view.OpensAt), easternTime(view.CutoffAt)
	return &view, dbID, nil
}

// loadStackPlay reads the signed-in player's run; lock holds the row for the caller's transaction.
func (s *Server) loadStackPlay(ctx context.Context, q rowQuerier, dbID int64, discordID string, lock bool) (stackPlay, error) {
	var p stackPlay
	query := `SELECT sp.height, sp.best_height, sp.drops, sp.started_at, sp.finished_at FROM stack_plays sp
		JOIN participants p ON p.id = sp.participant_id JOIN instances i ON i.id = p.instance_id
		WHERE sp.game_id = $1 AND i.public_id = $2 AND p.discord_user_id = $3`
	if lock {
		query += ` FOR UPDATE OF sp`
	}
	err := q.QueryRow(ctx, query, dbID, s.public.InstanceID, discordID).Scan(&p.height, &p.best, &p.drops, &p.started, &p.finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	p.exists = err == nil
	return p, err
}

// stackPage: the scored game while it is open and the player hasn't finished, otherwise their run or the results.
// Free play (?practice=1) opens to players only once a game has closed; admins can always use it to test.
func (s *Server) stackPage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed {
		renderSite(c, "home.html", data)
		return
	}
	ctx := c.Request.Context()
	if c.Param("gameID") == "" && c.Query("practice") == "1" {
		free, err := data.Admin, error(nil)
		if !free {
			free, err = s.stackFreePlay(ctx)
		}
		if err != nil {
			c.String(http.StatusInternalServerError, "could not load the page")
			return
		}
		if free {
			s.serveStackBoard(c, "")
			return
		}
	}
	view, dbID, err := s.loadStack(c)
	if errors.Is(err, errScrambleNotFound) {
		if c.Param("gameID") == "" {
			c.Redirect(http.StatusSeeOther, "/games")
			return
		}
		c.String(http.StatusNotFound, "game not found")
		return
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	play, err := s.loadStackPlay(ctx, s.pool, dbID, data.User.DiscordUserID, false)
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	if view.Open && play.finished == nil {
		attrs := fmt.Sprintf(` data-action="/games/stack-em-up/%s"`, view.ID)
		if play.exists {
			attrs += fmt.Sprintf(` data-resume="1" data-height="%d" data-drops="%d" data-elapsed="%d"`,
				play.height, play.drops, s.now().Sub(play.started).Milliseconds())
		}
		s.serveStackBoard(c, attrs)
		return
	}
	if play.exists {
		view.Played, view.Best, view.Drops = true, play.best, play.drops
		if play.finished != nil {
			view.Finished, view.Time = true, tiltClock(play.finished.Sub(play.started))
		}
	}
	if !view.Open && !view.Upcoming && !view.ResultsWait {
		players, err := loadStackPlayers(ctx, s.pool, dbID)
		if err != nil {
			c.String(http.StatusInternalServerError, "could not load the page")
			return
		}
		view.Results = stackResults(players)
	}
	data.Stack = view
	renderSite(c, "stack_em_up.html", data)
}

func stackResults(players []stackPlayer) []stackResult {
	places, points := stackPoints(players)
	out := make([]stackResult, 0, len(players))
	for _, p := range players {
		r := stackResult{Place: places[p.ID], ID: p.ID, Name: p.Name, Tribe: p.Tribe, Finished: p.Finished, Best: p.Best,
			Drops: p.Drops, Points: points[p.ID], PlacePoints: tiltPlacePoints(places[p.ID])}
		if p.Finished {
			r.Time, r.TimeMS = tiltClock(p.Time), p.Time.Milliseconds()
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { // places first, unfinished last (tallest first)
		a, b := out[i], out[j]
		if (a.Place == 0) != (b.Place == 0) {
			return a.Place != 0
		}
		if a.Place != b.Place {
			return a.Place < b.Place
		}
		return a.Best > b.Best
	})
	return out
}

// serveStackBoard sends the standalone game page; attrs (data-action etc.) go on <body> for the script.
func (s *Server) serveStackBoard(c *gin.Context, attrs string) {
	page, err := siteFiles.ReadFile("site/assets/stack-em-up.html")
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(strings.Replace(string(page), "<body>", "<body"+attrs+">", 1)))
}

// stackProgress saves a run after "Go", every block and every drop: the tower's height and the drop count. The
// first call starts the clock. Drops only go up; without a new drop the tower only grows. Height 8 is sent only
// once the full tower has been held, and finishes the run, which then can't change.
func (s *Server) stackProgress(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	view, dbID, err := s.loadStack(c)
	if errors.Is(err, errScrambleNotFound) {
		c.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if !data.Allowed || !view.Open {
		c.JSON(http.StatusConflict, errorResponse{Error: "this game is closed"})
		return
	}
	var req struct {
		Height int `json:"height"`
		Drops  int `json:"drops"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Height < 0 || req.Height > stackBlocks || req.Drops < 0 || req.Drops > stackMaxDrops {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "bad progress"})
		return
	}
	ctx := c.Request.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			requestLogger.Error("rollback stack em up progress", "error", err)
		}
	}()
	old, err := s.loadStackPlay(ctx, tx, dbID, data.User.DiscordUserID, true)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if old.finished != nil || req.Drops < old.drops || (req.Drops == old.drops && req.Height < old.height) {
		c.JSON(http.StatusConflict, errorResponse{Error: "already finished, or progress went backwards"})
		return
	}
	now := s.now()
	var finished any
	if req.Height == stackBlocks {
		// a finish needs a run already on the clock, and no one stacks and holds eight blocks this fast
		if !old.exists || now.Sub(old.started) < stackMinRun {
			c.JSON(http.StatusConflict, errorResponse{Error: "that run is too fast to count"})
			return
		}
		finished = now
	}
	tag, err := tx.Exec(ctx, `INSERT INTO stack_plays (game_id, participant_id, started_at, height, best_height, drops, finished_at)
		SELECT $1, p.id, $4, $5, $5, $6, $7 FROM participants p JOIN instances i ON i.id = p.instance_id WHERE i.public_id = $2 AND p.discord_user_id = $3
		ON CONFLICT (game_id, participant_id) DO UPDATE SET height = EXCLUDED.height,
			best_height = GREATEST(stack_plays.best_height, EXCLUDED.height), drops = EXCLUDED.drops, finished_at = EXCLUDED.finished_at`,
		dbID, s.public.InstanceID, data.User.DiscordUserID, now, req.Height, req.Drops, finished)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, errorResponse{Error: "not a player in this game"})
		return
	}
	if err := tx.Commit(ctx); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	resp := gin.H{"height": req.Height, "drops": req.Drops}
	if req.Height == stackBlocks {
		started := now
		if old.exists {
			started = old.started
		}
		resp["time_ms"], resp["time"] = now.Sub(started).Milliseconds(), tiltClock(now.Sub(started))
	}
	c.JSON(http.StatusOK, resp)
}

// stackFreePlay says whether any Stack 'Em Up game has closed.
func (s *Server) stackFreePlay(ctx context.Context) (bool, error) {
	var free bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stack_games g JOIN instances i ON i.id = g.instance_id
		WHERE i.public_id = $1 AND g.cutoff_at <= $2)`, s.public.InstanceID, s.now()).Scan(&free)
	return free, err
}

// addStackGames lists an instance's Stack 'Em Up games on /games.
func (s *Server) addStackGames(ctx context.Context, data *sitePageData, instanceID string) error {
	rows, err := s.pool.Query(ctx, `SELECT g.public_id::text, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM stack_games g JOIN instances i ON i.id = g.instance_id WHERE i.public_id::text = $1 ORDER BY g.opens_at`, instanceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	now := s.now()
	for rows.Next() {
		var id string
		var episode int32
		var opens, cutoff time.Time
		var resolved bool
		if err := rows.Scan(&id, &episode, &opens, &cutoff, &resolved); err != nil {
			return err
		}
		data.addGame(&episode, gameEntry{Kind: "Stack 'Em Up", Name: "Stack 'Em Up", URL: "/games/stack-em-up/" + id, Badge: "Scored", Status: gameStatus(now, opens, cutoff, resolved)})
		data.StackFreePlay = data.StackFreePlay || !now.Before(cutoff)
	}
	return rows.Err()
}

// stackProfileGames is a player's scored Stack 'Em Up results.
func (s *Server) stackProfileGames(ctx context.Context, participantID string) ([]profileGame, error) {
	rows, err := s.pool.Query(ctx, `SELECT g.id, g.public_id::text, g.episode_number FROM stack_games g JOIN stack_plays sp ON sp.game_id = g.id
		JOIN participants p ON p.id = sp.participant_id WHERE p.public_id = $1 AND g.resolved_at IS NOT NULL`, participantID)
	if err != nil {
		return nil, err
	}
	type ref struct {
		dbID    int64
		id      string
		episode int32
	}
	refs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ref, error) {
		var r ref
		return r, row.Scan(&r.dbID, &r.id, &r.episode)
	})
	if err != nil {
		return nil, err
	}
	var games []profileGame
	for _, r := range refs {
		players, err := loadStackPlayers(ctx, s.pool, r.dbID)
		if err != nil {
			return nil, err
		}
		_, points := stackPoints(players)
		for _, p := range players {
			if p.ID != participantID {
				continue
			}
			result := fmt.Sprintf("%d/%d stacked", p.Best, stackBlocks)
			if p.Finished {
				result = tiltClock(p.Time)
			}
			games = append(games, profileGame{Episode: r.episode, Kind: "Stack 'Em Up", URL: "/games/stack-em-up/" + r.id,
				Result: fmt.Sprintf("%s · %d %s · %+d pts", result, p.Drops, map[bool]string{true: "drop", false: "drops"}[p.Drops == 1], points[p.ID])})
		}
	}
	return games, nil
}
