package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sort"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// buttonAwards scores Press the Button. Players with zero presses have no result (they aren't in presses).
// Players who share a press count earn the group's size, at most 3. Everyone else is ranked by distinct
// press count: the most earns +2, the second most -1 and the least +1. A count gets only the first that
// applies, so with two distinct counts the lower one is "second most". On top of that, everyone earns +1
// per order of magnitude past 10 presses (100: +1, 1,000: +2, 10,000+: +3).
func buttonAwards(presses map[string]int64) map[string]int {
	awards := map[string]int{}
	for player, r := range buttonRanks(presses) {
		if points := r.Points + pressBonus(presses[player]); points != 0 {
			awards[player] = points
		}
	}
	return awards
}

// buttonRank is the press-count rule a player hit: "tie" (Group players share the count), "most", "second",
// "least", or "" for none.
type buttonRank struct {
	Rule   string
	Group  int
	Points int
}

func buttonRanks(presses map[string]int64) map[string]buttonRank {
	byCount := map[int64][]string{}
	for player, n := range presses {
		byCount[n] = append(byCount[n], player)
	}
	counts := make([]int64, 0, len(byCount))
	for n := range byCount {
		counts = append(counts, n)
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i] > counts[j] })
	ranks := map[string]buttonRank{}
	for i, n := range counts {
		group := byCount[n]
		var r buttonRank
		switch {
		case len(group) > 1:
			r = buttonRank{"tie", len(group), min(len(group), 3)}
		case i == 0:
			r = buttonRank{"most", 1, 2}
		case i == 1:
			r = buttonRank{"second", 1, -1}
		case i == len(counts)-1:
			r = buttonRank{"least", 1, 1}
		}
		for _, player := range group {
			ranks[player] = r
		}
	}
	return ranks
}

func pressBonus(presses int64) int {
	bonus := 0
	for n := int64(100); presses >= n && bonus < 3; n *= 10 {
		bonus++
	}
	return bonus
}

type buttonGame struct {
	ID       uuid.UUID `json:"id"`
	Episode  *int32    `json:"episode_number"` // nil for an admin-only, unscored test game
	OpensAt  time.Time `json:"opens_at"`
	CutoffAt time.Time `json:"cutoff_at"`
	Resolved bool      `json:"resolved"`
}

// createButtonGame creates (or, before it resolves, reschedules) an instance's game for an episode. Without
// an episode it creates a new admin-only test game, which never scores.
func (s *Server) createButtonGame(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok || !s.requireInstanceAdminRequest(c, instanceID) {
		return
	}
	var req struct {
		Episode  *int32    `json:"episode_number"`
		OpensAt  time.Time `json:"opens_at" binding:"required"`
		CutoffAt time.Time `json:"cutoff_at" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !req.CutoffAt.After(req.OpensAt) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "opens_at and a later cutoff_at are required"})
		return
	}
	game := buttonGame{Episode: req.Episode}
	conflict := ""
	if req.Episode != nil {
		conflict = `ON CONFLICT (instance_id, episode_number) DO UPDATE SET opens_at = EXCLUDED.opens_at, cutoff_at = EXCLUDED.cutoff_at
			WHERE button_games.resolved_at IS NULL`
	}
	err := s.pool.QueryRow(c.Request.Context(), `INSERT INTO button_games (instance_id, episode_number, opens_at, cutoff_at)
		SELECT id, $2, $3, $4 FROM instances WHERE public_id = $1 `+conflict+`
		RETURNING public_id, opens_at, cutoff_at, resolved_at IS NOT NULL`, instanceID, req.Episode, req.OpensAt, req.CutoffAt).
		Scan(&game.ID, &game.OpensAt, &game.CutoffAt, &game.Resolved)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusConflict, errorResponse{Error: "instance not found, or this episode's game already resolved"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "could not save game"})
		return
	}
	c.JSON(http.StatusOK, game)
}

func (s *Server) resolveButtonGameRequest(c *gin.Context) {
	gameID, ok := parseUUIDPath(c, "gameID")
	if !ok {
		return
	}
	var instanceID uuid.UUID
	err := s.pool.QueryRow(c.Request.Context(), `SELECT i.public_id FROM button_games g JOIN instances i ON i.id = g.instance_id WHERE g.public_id = $1`, gameID).Scan(&instanceID)
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
	awards, err := s.resolveButtonGame(c.Request.Context(), gameID)
	if err != nil {
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"awards": awards})
}

var errButtonOpen = errors.New("game cannot resolve before its cutoff")

// resolveButtonGame writes each player's award to the bonus ledger once; later calls return nil awards.
func (s *Server) resolveButtonGame(ctx context.Context, gameID uuid.UUID) (map[string]int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			requestLogger.Error("rollback button game", "error", err)
		}
	}()
	var game struct {
		id            int64
		instance      uuid.UUID
		episode       *int32
		opens, cutoff time.Time
		resolved      bool
	}
	if err := tx.QueryRow(ctx, `SELECT g.id, i.public_id, g.episode_number, g.opens_at, g.cutoff_at, g.resolved_at IS NOT NULL
		FROM button_games g JOIN instances i ON i.id = g.instance_id WHERE g.public_id = $1 FOR UPDATE OF g`, gameID).
		Scan(&game.id, &game.instance, &game.episode, &game.opens, &game.cutoff, &game.resolved); err != nil {
		return nil, err
	}
	if game.resolved {
		return nil, nil
	}
	if s.now().Before(game.cutoff) {
		return nil, errButtonOpen
	}
	players, err := loadButtonPlayers(ctx, tx, game.id, game.cutoff)
	if err != nil {
		return nil, err
	}
	scores := buttonScores(players)
	awards := map[string]int{}
	for id, score := range scores {
		if score.Total != 0 {
			awards[id] = score.Total
		}
	}
	if len(awards) > 0 && game.episode != nil { // test games report what they would award, but never score
		q := s.queries.WithTx(tx)
		at := pgtype.Timestamptz{Time: game.cutoff, Valid: true}
		name := fmt.Sprintf("Press the Button: Episode %d", *game.episode)
		activity, err := q.CreateInstanceActivity(ctx, db.CreateInstanceActivityParams{
			ActivityType: "press_the_button", Name: name, Status: "completed", Metadata: []byte("{}"),
			StartsAt: pgtype.Timestamptz{Time: game.opens, Valid: true}, EndsAt: at, InstanceID: toPGUUID(game.instance),
		})
		if err != nil {
			return nil, err
		}
		occurrence, err := q.CreateActivityOccurrence(ctx, db.CreateActivityOccurrenceParams{
			OccurrenceType: "press_the_button", Name: name, EffectiveAt: at, StartsAt: activity.StartsAt, EndsAt: at,
			Status: "resolved", Metadata: []byte("{}"), ActivityID: activity.ID,
		})
		if err != nil {
			return nil, err
		}
		for player, points := range awards {
			sc := scores[player]
			metadata, err := json.Marshal(map[string]int64{"presses": sc.Presses, "place_volume": int64(sc.PlaceVolume),
				"streak_days": int64(sc.StreakDays), "streak": int64(sc.Streak), "first": int64(sc.First), "last": int64(sc.Last)})
			if err != nil {
				return nil, err
			}
			if _, err := q.CreateBonusPointLedgerEntry(ctx, db.CreateBonusPointLedgerEntryParams{
				EntryKind: "award", Points: int32(points), Visibility: "public", Reason: name, EffectiveAt: at, // #nosec G115 -- points are in [-2, 10]
				AwardKey: pgtype.Text{String: "press_the_button", Valid: true}, Metadata: metadata,
				InstanceID: toPGUUID(game.instance), ParticipantID: toPGUUID(uuid.MustParse(player)), ActivityOccurrenceID: occurrence.ID,
			}); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE button_games SET resolved_at = $2 WHERE id = $1`, game.id, s.now()); err != nil {
		return nil, err
	}
	return awards, tx.Commit(ctx)
}

// resolveDueButtonGames scores every game past its cutoff; RunButtonResolver calls it each minute.
func (s *Server) resolveDueButtonGames(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT public_id FROM button_games WHERE resolved_at IS NULL AND cutoff_at <= $1 AND episode_number IS NOT NULL`, s.now())
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.resolveButtonGame(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// ResolveDueGames scores every scheduled Press the Button, Spell It Out, Inside Track and Free the Block game past its cutoff.
func (s *Server) ResolveDueGames(ctx context.Context) error {
	if err := s.resolveDueButtonGames(ctx); err != nil {
		return err
	}
	if err := s.resolveDueScrambleGames(ctx); err != nil {
		return err
	}
	if err := s.resolveDueTrackGames(ctx); err != nil {
		return err
	}
	if err := s.resolveDueFreeTheBlockGames(ctx); err != nil {
		return err
	}
	return s.resolveDueCastawordleGames(ctx)
}

// resolveDueCastawordleGames scores every scored Castawordle round past its cutoff.
func (s *Server) resolveDueCastawordleGames(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT g.public_id FROM castawordle_games g
		JOIN activity_occurrences o ON o.public_id = g.wordle_round_id
		WHERE o.status = 'recorded' AND g.cutoff_at <= $1`, s.now())
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, id := range ids {
		game, err := s.queries.GetCastawordleGame(ctx, toPGUUID(id))
		if err != nil {
			return err
		}
		if _, err := s.resolveCastawordleGame(ctx, game, nil); err != nil {
			return err
		}
	}
	return nil
}

// RunButtonResolver resolves Press the Button and Spell It Out games at their cutoff until ctx ends.
func (s *Server) RunButtonResolver(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if err := s.resolveDueButtonGames(ctx); err != nil && ctx.Err() == nil {
			requestLogger.Error("resolve button games", "error", err)
		}
		if err := s.resolveDueScrambleGames(ctx); err != nil && ctx.Err() == nil {
			requestLogger.Error("resolve spell it out games", "error", err)
		}
		if err := s.resolveDueTrackGames(ctx); err != nil && ctx.Err() == nil {
			requestLogger.Error("resolve inside track games", "error", err)
		}
		if err := s.resolveDueFreeTheBlockGames(ctx); err != nil && ctx.Err() == nil {
			requestLogger.Error("resolve free the block games", "error", err)
		}
		if err := s.resolveDueCastawordleGames(ctx); err != nil && ctx.Err() == nil {
			requestLogger.Error("resolve castawordle games", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// buttonPresses is the signed-in player's press count in a game.
func (s *Server) buttonPresses(ctx context.Context, gameID int64, discordID string) int64 {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT b.presses FROM button_presses b JOIN participants p ON p.id = b.participant_id
		WHERE b.game_id = $1 AND p.discord_user_id = $2`, gameID, discordID).Scan(&n)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		requestLogger.Error("load button presses", "error", err)
	}
	return n
}

func (s *Server) buttonPage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed {
		renderSite(c, "home.html", data)
		return
	}
	view, dbID, err := s.loadButtonView(c, data)
	if errors.Is(err, errButtonNotFound) {
		if c.Param("gameID") == "" { // nothing open right now
			c.Redirect(http.StatusFound, "/games")
			return
		}
		c.String(http.StatusNotFound, "game not found")
		return
	}
	var resolved bool
	if err == nil {
		err = s.pool.QueryRow(c.Request.Context(), `SELECT resolved_at IS NOT NULL FROM button_games WHERE id = $1`, dbID).Scan(&resolved)
	}
	if err == nil {
		err = s.fillButtonView(c.Request.Context(), view, dbID, data.User.DiscordUserID, resolved)
	}
	if err != nil {
		c.String(http.StatusInternalServerError, "could not load the page")
		return
	}
	data.Button = view
	renderSite(c, "button.html", data)
}

// pressButton counts one press for the signed-in player. The page's script gets JSON (count and tribe
// totals); plain form posts redirect back to the button.
func (s *Server) pressButton(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	view, gameID, err := s.loadButtonView(c, data)
	if errors.Is(err, errButtonNotFound) {
		c.Status(http.StatusNotFound)
		return
	}
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if !data.Allowed || !view.Open {
		c.Status(http.StatusConflict)
		return
	}
	var count int64
	now := s.now()
	day := easternTime(now).Format(time.DateOnly)
	var newDay bool // first press today? (only picks a hint, so no lock needed)
	if err := s.pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM button_presses b JOIN participants p ON p.id = b.participant_id
		WHERE b.game_id = $1 AND p.discord_user_id = $2 AND $3::date = ANY(b.press_days))`, gameID, data.User.DiscordUserID, day).Scan(&newDay); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO button_presses (game_id, participant_id, presses, first_press_at, last_press_at, press_days)
		SELECT $1, p.id, 1, $4, $4, ARRAY[$5::date] FROM participants p JOIN instances i ON i.id = p.instance_id
		WHERE i.public_id = $2 AND p.discord_user_id = $3
		ON CONFLICT (game_id, participant_id) DO UPDATE SET presses = button_presses.presses + 1,
			first_press_at = COALESCE(button_presses.first_press_at, $4), last_press_at = $4,
			press_days = CASE WHEN $5::date = ANY(button_presses.press_days) THEN button_presses.press_days ELSE button_presses.press_days || $5::date END
		RETURNING presses`, gameID, s.public.InstanceID, data.User.DiscordUserID, now, day).Scan(&count)
	if err != nil {
		c.Status(http.StatusConflict)
		return
	}
	if c.GetHeader("X-Press") != "" {
		players, err := loadButtonPlayers(ctx, s.pool, gameID, now)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		var me string
		if err := s.pool.QueryRow(ctx, `SELECT p.public_id::text FROM participants p JOIN instances i ON i.id = p.instance_id
			WHERE i.public_id = $1 AND p.discord_user_id = $2`, s.public.InstanceID, data.User.DiscordUserID).Scan(&me); err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		today, err := time.Parse(time.DateOnly, day)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		hint := buttonHint(players, me, newDay, today, func(lines []string) string { return lines[rand.IntN(len(lines))] }) // #nosec G404 -- flavor text
		c.JSON(http.StatusOK, gin.H{"count": count, "tribes": tribePresses(players), "hint": hint})
		return
	}
	c.Redirect(http.StatusSeeOther, view.Action)
}

// createButtonTest starts a one-hour admin-only test game and opens it.
func (s *Server) createButtonTest(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Admin {
		c.Status(http.StatusNotFound)
		return
	}
	var id uuid.UUID
	now := s.now()
	if err := s.pool.QueryRow(c.Request.Context(), `INSERT INTO button_games (instance_id, opens_at, cutoff_at)
		SELECT id, $2, $3 FROM instances WHERE public_id = $1 RETURNING public_id`, s.public.InstanceID, now, now.Add(time.Hour)).Scan(&id); err != nil {
		c.String(http.StatusInternalServerError, "could not create a test game")
		return
	}
	c.Redirect(http.StatusSeeOther, "/games/button/"+id.String())
}
