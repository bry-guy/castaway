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
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/freetheblock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Free the Block: slide the blocks to free the idol (internal/freetheblock, site/assets/free-the-block.js). One
// scored run per player. The page posts the whole move list after every move, and the server replays it from
// the start: it alone counts moves and decides whether the idol is out. A run stays open until it's solved,
// finished, or the cutoff passes. League points: +1 for solving, fewest moves +2, second fewest +1 (ties share
// the place, as Island Scramble does).

const ftbMaxMoves = 2000 // a run is about 120 slides; this only bounds a hostile request

type ftbPlayer struct {
	ID, Name, Tribe string
	Moves           int
	Solved          bool
}

// ftbPoints ranks solvers by moves (ties share the place); non-solvers have no place and score 0.
func ftbPoints(players []ftbPlayer) (map[string]int, map[string]int) {
	places, points := map[string]int{}, map[string]int{}
	for _, p := range players {
		if !p.Solved {
			places[p.ID], points[p.ID] = 0, 0
			continue
		}
		place := 1
		for _, o := range players {
			if o.Solved && o.Moves < p.Moves {
				place++
			}
		}
		places[p.ID], points[p.ID] = place, 1+max(3-place, 0)
	}
	return places, points
}

// loadFreeTheBlockPlayers replays each stored run; a stored run is always one Replay accepted when it was saved.
func loadFreeTheBlockPlayers(ctx context.Context, q scrambleQuerier, gameID int64) ([]ftbPlayer, error) {
	rows, err := q.Query(ctx, `SELECT p.public_id::text, p.name, COALESCE(t.name, ''), fp.moves
		FROM free_the_block_plays fp JOIN participants p ON p.id = fp.participant_id JOIN free_the_block_games g ON g.id = fp.game_id
		LEFT JOIN LATERAL (SELECT pg.name FROM participant_group_membership_periods m JOIN participant_groups pg ON pg.id = m.participant_group_id
			WHERE m.participant_id = p.id AND pg.kind = 'tribe' AND m.starts_at <= g.cutoff_at AND (m.ends_at IS NULL OR m.ends_at > g.cutoff_at)
			ORDER BY m.starts_at DESC LIMIT 1) t ON true
		WHERE fp.game_id = $1 ORDER BY p.name`, gameID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ftbPlayer, error) {
		var p ftbPlayer
		var encoded string
		if err := row.Scan(&p.ID, &p.Name, &p.Tribe, &encoded); err != nil {
			return p, err
		}
		moves, err := freetheblock.Decode(encoded)
		if err != nil {
			return p, err
		}
		res, err := freetheblock.Replay(moves)
		p.Moves, p.Solved = res.Moves, res.Solved
		return p, err
	})
}

type freeTheBlockGame struct {
	ID       uuid.UUID `json:"id"`
	Episode  int32     `json:"episode_number"`
	OpensAt  time.Time `json:"opens_at"`
	CutoffAt time.Time `json:"cutoff_at"`
	Resolved bool      `json:"resolved"`
}

// createFreeTheBlockGame creates (or, before anyone plays it, reschedules) an instance's game for an episode.
func (s *Server) createFreeTheBlockGame(c *gin.Context) {
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
	game := freeTheBlockGame{Episode: req.Episode}
	err := s.pool.QueryRow(c.Request.Context(), `INSERT INTO free_the_block_games (instance_id, episode_number, opens_at, cutoff_at)
		SELECT id, $2, $3, $4 FROM instances WHERE public_id = $1
		ON CONFLICT (instance_id, episode_number) DO UPDATE SET opens_at = EXCLUDED.opens_at, cutoff_at = EXCLUDED.cutoff_at
			WHERE free_the_block_games.resolved_at IS NULL AND NOT EXISTS (SELECT 1 FROM free_the_block_plays p WHERE p.game_id = free_the_block_games.id)
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

func (s *Server) resolveFreeTheBlockGameRequest(c *gin.Context) {
	gameID, ok := parseUUIDPath(c, "gameID")
	if !ok {
		return
	}
	var instanceID uuid.UUID
	err := s.pool.QueryRow(c.Request.Context(), `SELECT i.public_id FROM free_the_block_games g JOIN instances i ON i.id = g.instance_id WHERE g.public_id = $1`, gameID).Scan(&instanceID)
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
	awards, err := s.resolveFreeTheBlockGame(c.Request.Context(), gameID)
	if err != nil {
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"awards": awards})
}

// resolveFreeTheBlockGame writes every player's points to the bonus ledger once; later calls return nil.
func (s *Server) resolveFreeTheBlockGame(ctx context.Context, gameID uuid.UUID) (map[string]int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			requestLogger.Error("rollback free the block game", "error", err)
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
		FROM free_the_block_games g JOIN instances i ON i.id = g.instance_id WHERE g.public_id = $1 FOR UPDATE OF g`, gameID).
		Scan(&id, &instance, &episode, &opens, &cutoff, &resolved); err != nil {
		return nil, err
	}
	if resolved {
		return nil, nil
	}
	if s.now().Before(cutoff) {
		return nil, errScrambleOpen
	}
	players, err := loadFreeTheBlockPlayers(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	places, awards := ftbPoints(players)
	if len(players) > 0 {
		q := s.queries.WithTx(tx)
		at := pgtype.Timestamptz{Time: cutoff, Valid: true}
		name := fmt.Sprintf("Free the Block: Episode %d", episode)
		activity, err := q.CreateInstanceActivity(ctx, db.CreateInstanceActivityParams{
			ActivityType: "free_the_block", Name: name, Status: "completed", Metadata: []byte("{}"),
			StartsAt: pgtype.Timestamptz{Time: opens, Valid: true}, EndsAt: at, InstanceID: toPGUUID(instance),
		})
		if err != nil {
			return nil, err
		}
		occurrence, err := q.CreateActivityOccurrence(ctx, db.CreateActivityOccurrenceParams{
			OccurrenceType: "free_the_block", Name: name, EffectiveAt: at, StartsAt: activity.StartsAt, EndsAt: at,
			Status: "resolved", Metadata: []byte("{}"), ActivityID: activity.ID,
		})
		if err != nil {
			return nil, err
		}
		for _, p := range players {
			if awards[p.ID] == 0 {
				continue
			}
			metadata, err := json.Marshal(map[string]any{"place": places[p.ID], "points": awards[p.ID], "moves": p.Moves, "solved": p.Solved})
			if err != nil {
				return nil, err
			}
			if _, err := q.CreateBonusPointLedgerEntry(ctx, db.CreateBonusPointLedgerEntryParams{
				EntryKind: "award", Points: int32(awards[p.ID]), Visibility: "public", Reason: name, EffectiveAt: at, // #nosec G115 -- points are 1–4
				AwardKey: pgtype.Text{String: "free_the_block", Valid: true}, Metadata: metadata,
				InstanceID: toPGUUID(instance), ParticipantID: toPGUUID(uuid.MustParse(p.ID)), ActivityOccurrenceID: occurrence.ID,
			}); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE free_the_block_games SET resolved_at = $2 WHERE id = $1`, id, s.now()); err != nil {
		return nil, err
	}
	return awards, tx.Commit(ctx)
}

func (s *Server) resolveDueFreeTheBlockGames(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT public_id FROM free_the_block_games WHERE resolved_at IS NULL AND cutoff_at <= $1`, s.now())
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.resolveFreeTheBlockGame(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

type freeTheBlockView struct {
	ID          string
	Episode     int32
	Open        bool
	Upcoming    bool
	OpensAt     time.Time
	CutoffAt    time.Time
	Played      bool // has a run
	Solved      bool
	Moves       int
	Results     []freeTheBlockResult
	ResultsWait bool
}

type freeTheBlockResult struct {
	Place  int
	ID     string
	Name   string
	Tribe  string
	Moves  int
	Solved bool
	Points int
}

type freeTheBlockPlay struct {
	moves    string
	finished bool
	exists   bool
}

// loadFreeTheBlock finds /games/free-the-block/:gameID, or the scheduled game that's open now (else the latest one).
func (s *Server) loadFreeTheBlock(c *gin.Context) (*freeTheBlockView, int64, error) {
	var view freeTheBlockView
	var dbID int64
	var resolved bool
	query := `SELECT g.id, g.public_id::text, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM free_the_block_games g JOIN instances i ON i.id = g.instance_id WHERE i.public_id = $1 AND `
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

func (s *Server) loadFreeTheBlockPlay(ctx context.Context, dbID int64, discordID string) (freeTheBlockPlay, error) {
	var p freeTheBlockPlay
	err := s.pool.QueryRow(ctx, `SELECT fp.moves, fp.finished_at IS NOT NULL FROM free_the_block_plays fp
		JOIN participants p ON p.id = fp.participant_id JOIN instances i ON i.id = p.instance_id
		WHERE fp.game_id = $1 AND i.public_id = $2 AND p.discord_user_id = $3`, dbID, s.public.InstanceID, discordID).Scan(&p.moves, &p.finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	p.exists = err == nil
	return p, err
}

// freeTheBlockPage: the game while it's open and the player hasn't finished, otherwise their result or the
// results. Admins with no open game get the unscored practice board.
func (s *Server) freeTheBlockPage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed {
		renderSite(c, "home.html", data)
		return
	}
	if data.Admin && c.Param("gameID") == "" && c.Query("practice") == "1" { // unscored board even while a game is open
		s.serveFreeTheBlockBoard(c, "")
		return
	}
	ctx := c.Request.Context()
	view, dbID, err := s.loadFreeTheBlock(c)
	if errors.Is(err, errScrambleNotFound) {
		if c.Param("gameID") == "" && data.Admin {
			s.serveFreeTheBlockBoard(c, "")
			return
		}
		if c.Param("gameID") == "" {
			c.Redirect(http.StatusFound, "/games")
			return
		}
		c.String(http.StatusNotFound, "game not found")
		return
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	play, err := s.loadFreeTheBlockPlay(ctx, dbID, data.User.DiscordUserID)
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	if view.Open && !play.finished {
		attrs := fmt.Sprintf(` data-action="/games/free-the-block/%s"`, view.ID)
		if play.exists {
			attrs += fmt.Sprintf(` data-moves="%s"`, play.moves)
		}
		s.serveFreeTheBlockBoard(c, attrs)
		return
	}
	if play.exists {
		moves, err := freetheblock.Decode(play.moves)
		if err != nil {
			c.String(http.StatusInternalServerError, "could not load the page")
			return
		}
		res, err := freetheblock.Replay(moves)
		if err != nil {
			c.String(http.StatusInternalServerError, "could not load the page")
			return
		}
		view.Played, view.Solved, view.Moves = true, res.Solved, res.Moves
	}
	if !view.Open && !view.Upcoming && !view.ResultsWait {
		players, err := loadFreeTheBlockPlayers(ctx, s.pool, dbID)
		if err != nil {
			c.String(http.StatusInternalServerError, "could not load the page")
			return
		}
		view.Results = freeTheBlockResults(players)
	}
	data.FreeTheBlock = view
	renderSite(c, "free_the_block.html", data)
}

func freeTheBlockResults(players []ftbPlayer) []freeTheBlockResult {
	places, points := ftbPoints(players)
	out := make([]freeTheBlockResult, 0, len(players))
	for _, p := range players {
		out = append(out, freeTheBlockResult{Place: places[p.ID], ID: p.ID, Name: p.Name, Tribe: p.Tribe, Moves: p.Moves, Solved: p.Solved, Points: points[p.ID]})
	}
	sort.SliceStable(out, func(i, j int) bool { // places first, unsolved last
		a, b := out[i].Place, out[j].Place
		return a != 0 && (b == 0 || a < b)
	})
	return out
}

// serveFreeTheBlockBoard sends the standalone game page; attrs (data-action etc.) go on <body> for the script.
func (s *Server) serveFreeTheBlockBoard(c *gin.Context, attrs string) {
	page, err := siteFiles.ReadFile("site/assets/free-the-block.html")
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(strings.Replace(string(page), "<body>", "<body"+attrs+">", 1)))
}

// freeTheBlockProgress saves a run's whole move list. The list must extend the saved one, every move must be
// legal, and finishing needs the idol out. Once finished, the run can't change.
func (s *Server) freeTheBlockProgress(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	view, dbID, err := s.loadFreeTheBlock(c)
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
		Moves    []freetheblock.Move `json:"moves"`
		Finished bool                `json:"finished"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Moves) > ftbMaxMoves {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "bad progress"})
		return
	}
	res, err := freetheblock.Replay(req.Moves)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if req.Finished && !res.Solved {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "the idol isn't out yet"})
		return
	}
	var finished any
	if req.Finished {
		finished = s.now()
	}
	tag, err := s.pool.Exec(c.Request.Context(), `INSERT INTO free_the_block_plays (game_id, participant_id, started_at, moves, finished_at)
		SELECT $1, p.id, $4, $5, $6 FROM participants p JOIN instances i ON i.id = p.instance_id WHERE i.public_id = $2 AND p.discord_user_id = $3
		ON CONFLICT (game_id, participant_id) DO UPDATE SET moves = EXCLUDED.moves, finished_at = EXCLUDED.finished_at
			WHERE free_the_block_plays.finished_at IS NULL AND length(EXCLUDED.moves) >= length(free_the_block_plays.moves)
				AND left(EXCLUDED.moves, length(free_the_block_plays.moves)) = free_the_block_plays.moves`,
		dbID, s.public.InstanceID, data.User.DiscordUserID, s.now(), freetheblock.Encode(req.Moves), finished)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, errorResponse{Error: "not a player, already finished, or moves went backwards"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"moves": res.Moves, "solved": res.Solved})
}

// addFreeTheBlockGames lists an instance's Free the Block games on /games.
func (s *Server) addFreeTheBlockGames(ctx context.Context, data *sitePageData, instanceID string) error {
	rows, err := s.pool.Query(ctx, `SELECT g.public_id::text, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM free_the_block_games g JOIN instances i ON i.id = g.instance_id WHERE i.public_id::text = $1 ORDER BY g.opens_at`, instanceID)
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
		data.addGame(&episode, gameEntry{Kind: "Free the Block", Name: "Free the Block", URL: "/games/free-the-block/" + id, Badge: "Scored", Status: gameStatus(now, opens, cutoff, resolved)})
	}
	return rows.Err()
}

// freeTheBlockProfileGames is a player's scored Free the Block results.
func (s *Server) freeTheBlockProfileGames(ctx context.Context, participantID string) ([]profileGame, error) {
	rows, err := s.pool.Query(ctx, `SELECT g.id, g.public_id::text, g.episode_number FROM free_the_block_games g JOIN free_the_block_plays fp ON fp.game_id = g.id
		JOIN participants p ON p.id = fp.participant_id WHERE p.public_id = $1 AND g.resolved_at IS NOT NULL`, participantID)
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
		players, err := loadFreeTheBlockPlayers(ctx, s.pool, r.dbID)
		if err != nil {
			return nil, err
		}
		_, points := ftbPoints(players)
		for _, p := range players {
			if p.ID != participantID {
				continue
			}
			result := "Not solved · +0 pts"
			if p.Solved {
				result = fmt.Sprintf("Solved in %d moves · %+d pts", p.Moves, points[p.ID])
			}
			games = append(games, profileGame{Episode: r.episode, Kind: "Free the Block", URL: "/games/free-the-block/" + r.id, Result: result})
		}
	}
	return games, nil
}
