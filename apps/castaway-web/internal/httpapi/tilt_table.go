package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Tilt Table: tilt the phone to roll three balls, one at a time, into three holes (site/assets/tilt-table.js).
// One timed run per player per game. The page saves which holes are filled and how many rollbacks after every
// sink and every fall. The server keeps the clock (first save at "Go" to the third sink, so a reload doesn't
// pause it) and counts aces: a ball sunk with no rollback since the previous sink. The physics runs on the phone
// (shortcut: a determined player could post a fake run; fine for a friends' league, as with Inside Track).
// League points: every finisher +1, fastest +3, second fastest +2 (ties share the place, as Free the Block
// does), and +1 per ace whether or not the player finished.

const tiltMaxRollbacks = 10000 // only bounds a hostile request

type tiltPlayer struct {
	ID, Name, Tribe string
	Rollbacks, Aces int
	Holes           int
	Finished        bool
	Time            time.Duration // finished runs only
}

// tiltTenths is a run's time in the tenths of a second the page shows; places compare these.
func tiltTenths(d time.Duration) int64 { return d.Milliseconds() / 100 }

// tiltPlaces ranks finishers by time in tenths (dense: after a tie at first, the next time is still second);
// players who didn't finish have no place (0).
func tiltPlaces(players []tiltPlayer) map[string]int {
	places := map[string]int{}
	for _, p := range players {
		if !p.Finished {
			places[p.ID] = 0
			continue
		}
		better := map[int64]bool{}
		for _, o := range players {
			if o.Finished && tiltTenths(o.Time) < tiltTenths(p.Time) {
				better[tiltTenths(o.Time)] = true
			}
		}
		places[p.ID] = len(better) + 1
	}
	return places
}

// tiltPlacePoints: fastest +3, second fastest +2; no place (didn't finish) is 0.
func tiltPlacePoints(place int) int { return map[int]int{1: 3, 2: 2}[place] }

// tiltPoints gives each player's league points: +1 for finishing, place points, and +1 per ace.
func tiltPoints(players []tiltPlayer) (map[string]int, map[string]int) {
	places, points := tiltPlaces(players), map[string]int{}
	for _, p := range players {
		points[p.ID] = p.Aces
		if p.Finished {
			points[p.ID] += 1 + tiltPlacePoints(places[p.ID])
		}
	}
	return places, points
}

func loadTiltPlayers(ctx context.Context, q scrambleQuerier, gameID int64) ([]tiltPlayer, error) {
	rows, err := q.Query(ctx, `SELECT p.public_id::text, p.name, COALESCE(t.name, ''), tp.holes_mask, tp.rollbacks, tp.aces,
			COALESCE(EXTRACT(EPOCH FROM tp.finished_at - tp.started_at) * 1000, -1)::bigint
		FROM tilt_plays tp JOIN participants p ON p.id = tp.participant_id JOIN tilt_games g ON g.id = tp.game_id
		LEFT JOIN LATERAL (SELECT pg.name FROM participant_group_membership_periods m JOIN participant_groups pg ON pg.id = m.participant_group_id
			WHERE m.participant_id = p.id AND pg.kind = 'tribe' AND m.starts_at <= g.cutoff_at AND (m.ends_at IS NULL OR m.ends_at > g.cutoff_at)
			ORDER BY m.starts_at DESC LIMIT 1) t ON true
		WHERE tp.game_id = $1 ORDER BY p.name`, gameID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (tiltPlayer, error) {
		var p tiltPlayer
		var mask int
		var ms int64
		if err := row.Scan(&p.ID, &p.Name, &p.Tribe, &mask, &p.Rollbacks, &p.Aces, &ms); err != nil {
			return p, err
		}
		p.Holes = bits.OnesCount(uint(mask)) // #nosec G115 -- mask is 0–7
		p.Finished, p.Time = ms >= 0, time.Duration(ms)*time.Millisecond
		return p, nil
	})
}

type tiltGame struct {
	ID       uuid.UUID `json:"id"`
	Episode  int32     `json:"episode_number"`
	OpensAt  time.Time `json:"opens_at"`
	CutoffAt time.Time `json:"cutoff_at"`
	Resolved bool      `json:"resolved"`
}

// createTiltGame creates (or, before anyone plays it, reschedules) an instance's game for an episode.
func (s *Server) createTiltGame(c *gin.Context) {
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
	err := s.pool.QueryRow(c.Request.Context(), `INSERT INTO tilt_games (instance_id, episode_number, opens_at, cutoff_at)
		SELECT id, $2, $3, $4 FROM instances WHERE public_id = $1
		ON CONFLICT (instance_id, episode_number) DO UPDATE SET opens_at = EXCLUDED.opens_at, cutoff_at = EXCLUDED.cutoff_at
			WHERE tilt_games.resolved_at IS NULL AND NOT EXISTS (SELECT 1 FROM tilt_plays p WHERE p.game_id = tilt_games.id)
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

func (s *Server) resolveTiltGameRequest(c *gin.Context) {
	gameID, ok := parseUUIDPath(c, "gameID")
	if !ok {
		return
	}
	var instanceID uuid.UUID
	err := s.pool.QueryRow(c.Request.Context(), `SELECT i.public_id FROM tilt_games g JOIN instances i ON i.id = g.instance_id WHERE g.public_id = $1`, gameID).Scan(&instanceID)
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
	awards, err := s.resolveTiltGame(c.Request.Context(), gameID)
	if err != nil {
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"awards": awards})
}

// resolveTiltGame writes every player's points to the bonus ledger once; later calls return nil.
func (s *Server) resolveTiltGame(ctx context.Context, gameID uuid.UUID) (map[string]int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			requestLogger.Error("rollback tilt table game", "error", err)
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
		FROM tilt_games g JOIN instances i ON i.id = g.instance_id WHERE g.public_id = $1 FOR UPDATE OF g`, gameID).
		Scan(&id, &instance, &episode, &opens, &cutoff, &resolved); err != nil {
		return nil, err
	}
	if resolved {
		return nil, nil
	}
	if s.now().Before(cutoff) {
		return nil, errScrambleOpen
	}
	players, err := loadTiltPlayers(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	places, awards := tiltPoints(players)
	if len(players) > 0 {
		q := s.queries.WithTx(tx)
		at := pgtype.Timestamptz{Time: cutoff, Valid: true}
		name := fmt.Sprintf("Tilt Table: Episode %d", episode)
		activity, err := q.CreateInstanceActivity(ctx, db.CreateInstanceActivityParams{
			ActivityType: "tilt_table", Name: name, Status: "completed", Metadata: []byte("{}"),
			StartsAt: pgtype.Timestamptz{Time: opens, Valid: true}, EndsAt: at, InstanceID: toPGUUID(instance),
		})
		if err != nil {
			return nil, err
		}
		occurrence, err := q.CreateActivityOccurrence(ctx, db.CreateActivityOccurrenceParams{
			OccurrenceType: "tilt_table", Name: name, EffectiveAt: at, StartsAt: activity.StartsAt, EndsAt: at,
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
				"time_ms": p.Time.Milliseconds(), "rollbacks": p.Rollbacks, "aces": p.Aces})
			if err != nil {
				return nil, err
			}
			if _, err := q.CreateBonusPointLedgerEntry(ctx, db.CreateBonusPointLedgerEntryParams{
				EntryKind: "award", Points: int32(awards[p.ID]), Visibility: "public", Reason: name, EffectiveAt: at, // #nosec G115 -- points are 0–7
				AwardKey: pgtype.Text{String: "tilt_table", Valid: true}, Metadata: metadata,
				InstanceID: toPGUUID(instance), ParticipantID: toPGUUID(uuid.MustParse(p.ID)), ActivityOccurrenceID: occurrence.ID,
			}); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE tilt_games SET resolved_at = $2 WHERE id = $1`, id, s.now()); err != nil {
		return nil, err
	}
	return awards, tx.Commit(ctx)
}

func (s *Server) resolveDueTiltGames(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT public_id FROM tilt_games WHERE resolved_at IS NULL AND cutoff_at <= $1`, s.now())
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.resolveTiltGame(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

type tiltView struct {
	ID          string
	Episode     int32
	Open        bool
	Upcoming    bool
	OpensAt     time.Time
	CutoffAt    time.Time
	Played      bool // has a run
	Finished    bool
	Time        string
	Holes       int
	Rollbacks   int
	Aces        int
	Results     []tiltResult
	ResultsWait bool
}

type tiltResult struct {
	Place     int
	ID, Name  string
	Tribe     string
	Finished  bool
	Time      string
	TimeMS    int64
	Holes     int
	Rollbacks int
	Aces      int
	Points    int
	// The breakdown behind Points: place points (fastest +3, second +2); finishing adds +1 and each ace +1.
	PlacePoints int
}

type tiltPlay struct {
	mask, rollbacks, aces, sinkRollbacks int
	started                              time.Time
	finished                             *time.Time
	exists                               bool
}

// tiltClock formats a run's time the way the page shows it: m:ss.t.
func tiltClock(d time.Duration) string {
	t := tiltTenths(d)
	return fmt.Sprintf("%d:%02d.%d", t/600, t/10%60, t%10)
}

// loadTilt finds /games/tilt-table/:gameID, or the scheduled game that's open now (else the latest one).
func (s *Server) loadTilt(c *gin.Context) (*tiltView, int64, error) {
	var view tiltView
	var dbID int64
	var resolved bool
	query := `SELECT g.id, g.public_id::text, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM tilt_games g JOIN instances i ON i.id = g.instance_id WHERE i.public_id = $1 AND `
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

type rowQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadTiltPlay reads the signed-in player's run; lock holds the row for the caller's transaction.
func (s *Server) loadTiltPlay(ctx context.Context, q rowQueryer, dbID int64, discordID string, lock bool) (tiltPlay, error) {
	var p tiltPlay
	query := `SELECT tp.holes_mask, tp.rollbacks, tp.aces, tp.sink_rollbacks, tp.started_at, tp.finished_at FROM tilt_plays tp
		JOIN participants p ON p.id = tp.participant_id JOIN instances i ON i.id = p.instance_id
		WHERE tp.game_id = $1 AND i.public_id = $2 AND p.discord_user_id = $3`
	if lock {
		query += ` FOR UPDATE OF tp`
	}
	err := q.QueryRow(ctx, query, dbID, s.public.InstanceID, discordID).Scan(&p.mask, &p.rollbacks, &p.aces, &p.sinkRollbacks, &p.started, &p.finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	p.exists = err == nil
	return p, err
}

// tiltTablePage: free play (?practice=1, or no game yet), the scored board while a game is open and the player
// hasn't finished, otherwise their run or the results.
func (s *Server) tiltTablePage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed {
		renderSite(c, "home.html", data)
		return
	}
	if c.Param("gameID") == "" && c.Query("practice") == "1" { // free play: unscored for every player, even while a game is open
		s.serveTiltBoard(c, "")
		return
	}
	ctx := c.Request.Context()
	view, dbID, err := s.loadTilt(c)
	if errors.Is(err, errScrambleNotFound) {
		if c.Param("gameID") == "" {
			s.serveTiltBoard(c, "")
			return
		}
		c.String(http.StatusNotFound, "game not found")
		return
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	play, err := s.loadTiltPlay(ctx, s.pool, dbID, data.User.DiscordUserID, false)
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	if view.Open && play.finished == nil {
		attrs := fmt.Sprintf(` data-action="/games/tilt-table/%s"`, view.ID)
		if play.exists {
			fell := 0
			if play.rollbacks > play.sinkRollbacks {
				fell = 1
			}
			attrs += fmt.Sprintf(` data-resume="1" data-holes="%d" data-rollbacks="%d" data-aces="%d" data-fell="%d" data-elapsed="%d"`,
				play.mask, play.rollbacks, play.aces, fell, s.now().Sub(play.started).Milliseconds())
		}
		s.serveTiltBoard(c, attrs)
		return
	}
	if play.exists {
		view.Played, view.Holes, view.Rollbacks, view.Aces = true, bits.OnesCount(uint(play.mask)), play.rollbacks, play.aces // #nosec G115 -- mask is 0–7
		if play.finished != nil {
			view.Finished, view.Time = true, tiltClock(play.finished.Sub(play.started))
		}
	}
	if !view.Open && !view.Upcoming && !view.ResultsWait {
		players, err := loadTiltPlayers(ctx, s.pool, dbID)
		if err != nil {
			c.String(http.StatusInternalServerError, "could not load the page")
			return
		}
		view.Results = tiltResults(players)
	}
	data.Tilt = view
	renderSite(c, "tilt_table.html", data)
}

func tiltResults(players []tiltPlayer) []tiltResult {
	places, points := tiltPoints(players)
	out := make([]tiltResult, 0, len(players))
	for _, p := range players {
		r := tiltResult{Place: places[p.ID], ID: p.ID, Name: p.Name, Tribe: p.Tribe, Finished: p.Finished, Holes: p.Holes,
			Rollbacks: p.Rollbacks, Aces: p.Aces, Points: points[p.ID], PlacePoints: tiltPlacePoints(places[p.ID])}
		if p.Finished {
			r.Time, r.TimeMS = tiltClock(p.Time), p.Time.Milliseconds()
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { // places first, unfinished last (most holes first)
		a, b := out[i], out[j]
		if (a.Place == 0) != (b.Place == 0) {
			return a.Place != 0
		}
		if a.Place != b.Place {
			return a.Place < b.Place
		}
		return a.Holes > b.Holes
	})
	return out
}

// serveTiltBoard sends the standalone game page; attrs (data-action etc.) go on <body> for the script.
func (s *Server) serveTiltBoard(c *gin.Context, attrs string) {
	page, err := siteFiles.ReadFile("site/assets/tilt-table.html")
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(strings.Replace(string(page), "<body>", "<body"+attrs+">", 1)))
}

// tiltProgress saves a run after "Go", every fall and every sink: the filled holes and the rollback count. The
// first call starts the clock. Holes may only be added, one per call, and rollbacks only go up. A sink with no
// rollback since the previous sink is an ace. The third hole finishes the run, which then can't change.
func (s *Server) tiltProgress(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	view, dbID, err := s.loadTilt(c)
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
		HolesMask int `json:"holes_mask"`
		Rollbacks int `json:"rollbacks"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.HolesMask < 0 || req.HolesMask > 7 || req.Rollbacks < 0 || req.Rollbacks > tiltMaxRollbacks {
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
			requestLogger.Error("rollback tilt table progress", "error", err)
		}
	}()
	old, err := s.loadTiltPlay(ctx, tx, dbID, data.User.DiscordUserID, true)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	added := bits.OnesCount(uint(req.HolesMask &^ old.mask)) // #nosec G115 -- masks are 0–7
	if old.finished != nil || req.HolesMask&old.mask != old.mask || added > 1 || req.Rollbacks < old.rollbacks {
		c.JSON(http.StatusConflict, errorResponse{Error: "already finished, or progress went backwards"})
		return
	}
	aces, sinkRollbacks := old.aces, old.sinkRollbacks
	if added == 1 {
		if req.Rollbacks == old.sinkRollbacks {
			aces++
		}
		sinkRollbacks = req.Rollbacks
	}
	now := s.now()
	var finished any
	if req.HolesMask == 7 {
		finished = now
	}
	tag, err := tx.Exec(ctx, `INSERT INTO tilt_plays (game_id, participant_id, started_at, holes_mask, rollbacks, aces, sink_rollbacks, finished_at)
		SELECT $1, p.id, $4, $5, $6, $7, $8, $9 FROM participants p JOIN instances i ON i.id = p.instance_id WHERE i.public_id = $2 AND p.discord_user_id = $3
		ON CONFLICT (game_id, participant_id) DO UPDATE SET holes_mask = EXCLUDED.holes_mask, rollbacks = EXCLUDED.rollbacks,
			aces = EXCLUDED.aces, sink_rollbacks = EXCLUDED.sink_rollbacks, finished_at = EXCLUDED.finished_at`,
		dbID, s.public.InstanceID, data.User.DiscordUserID, now, req.HolesMask, req.Rollbacks, aces, sinkRollbacks, finished)
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
	resp := gin.H{"holes": bits.OnesCount(uint(req.HolesMask)), "rollbacks": req.Rollbacks, "aces": aces} // #nosec G115 -- mask is 0–7
	if req.HolesMask == 7 {
		started := now
		if old.exists {
			started = old.started
		}
		resp["time_ms"], resp["time"] = now.Sub(started).Milliseconds(), tiltClock(now.Sub(started))
	}
	c.JSON(http.StatusOK, resp)
}

// addTiltGames lists an instance's Tilt Table games on /games.
func (s *Server) addTiltGames(ctx context.Context, data *sitePageData, instanceID string) error {
	rows, err := s.pool.Query(ctx, `SELECT g.public_id::text, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM tilt_games g JOIN instances i ON i.id = g.instance_id WHERE i.public_id::text = $1 ORDER BY g.opens_at`, instanceID)
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
		data.addGame(&episode, gameEntry{Kind: "Tilt Table", Name: "Tilt Table", URL: "/games/tilt-table/" + id, Badge: "Scored", Status: gameStatus(now, opens, cutoff, resolved)})
	}
	return rows.Err()
}

// tiltProfileGames is a player's scored Tilt Table results.
func (s *Server) tiltProfileGames(ctx context.Context, participantID string) ([]profileGame, error) {
	rows, err := s.pool.Query(ctx, `SELECT g.id, g.public_id::text, g.episode_number FROM tilt_games g JOIN tilt_plays tp ON tp.game_id = g.id
		JOIN participants p ON p.id = tp.participant_id WHERE p.public_id = $1 AND g.resolved_at IS NOT NULL`, participantID)
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
		players, err := loadTiltPlayers(ctx, s.pool, r.dbID)
		if err != nil {
			return nil, err
		}
		_, points := tiltPoints(players)
		for _, p := range players {
			if p.ID != participantID {
				continue
			}
			result := strconv.Itoa(p.Holes) + "/3 in"
			if p.Finished {
				result = tiltClock(p.Time)
			}
			games = append(games, profileGame{Episode: r.episode, Kind: "Tilt Table", URL: "/games/tilt-table/" + r.id,
				Result: fmt.Sprintf("%s · %d %s · %+d pts", result, p.Aces, map[bool]string{true: "ace", false: "aces"}[p.Aces == 1], points[p.ID])})
		}
	}
	return games, nil
}
