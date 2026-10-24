package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// gameResult is one player's line in a closed, scored episode game. Fields not used by a game stay zero.
type gameResult struct {
	Name       string `json:"name"`
	Tribe      string `json:"tribe"`
	TribeEmoji string `json:"tribe_emoji"`
	Points     int    `json:"points"`
	// press_the_button
	Presses    int64 `json:"presses"`
	StreakDays int   `json:"streak_days"`
	First      bool  `json:"first"`
	Last       bool  `json:"last"`
	// The rule breakdown behind Points: press-count rule ("most", "second", "least", "tie" or ""), its
	// points, and the volume, streak, first and last bonuses.
	RankRule     string `json:"rank_rule"`
	RankPoints   int    `json:"rank_points"`
	VolumePoints int    `json:"volume_points"`
	StreakPoints int    `json:"streak_points"`
	// spell_it_out
	Place       int   `json:"place"` // 0: didn't solve
	Seconds     int64 `json:"seconds"`
	WrongChecks int   `json:"wrong_checks"`
	HintPoints  int   `json:"hint_points"`  // +1 for solving before the hint
	CleanPoints int   `json:"clean_points"` // fewest wrong checks +2, second +1; Points adds +1 for solving
	// inside_track (Place is shared with spell_it_out)
	Score     int `json:"score"` // cups + balls left once all three cups are in
	Cups      int `json:"cups"`
	BallsLeft int `json:"balls_left"`
	// free_the_block (Place is shared with spell_it_out; Solved is shared with castawordle)
	Moves int `json:"moves"`
	// castawordle
	Guesses int  `json:"guesses"`
	Solved  bool `json:"solved"`
}

// tribeResult is a tribe's figure for the game: total presses (button) or best-three average guesses
// (castawordle; a miss counts 7).
type tribeResult struct {
	Name  string  `json:"name"`
	Emoji string  `json:"emoji"`
	Value float64 `json:"value"`
}

// getGameResults serves a scored episode game's results for the weekly scores post. Unscored or open
// games are 409 so a post can't show partial results; answers are never included.
func (s *Server) getGameResults(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return
	}
	episode, err := strconv.Atoi(c.Param("episode"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "episode must be a number"})
		return
	}
	ctx := c.Request.Context()
	if err := requireWordleInstanceAdmin(ctx, s.queries, toPGUUID(instanceID), c.Request); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	kind, players, tribes, err := s.gameResults(ctx, instanceID, episode)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "no game for that episode"})
		return
	}
	if errors.Is(err, errGameNotScored) {
		c.JSON(http.StatusConflict, errorResponse{Error: err.Error()})
		return
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"type": kind, "players": players, "tribes": tribes})
}

var errGameNotScored = errors.New("the game isn't scored yet")

func (s *Server) gameResults(ctx context.Context, instanceID uuid.UUID, episode int) (string, []gameResult, []tribeResult, error) {
	emoji, err := s.tribeEmoji(ctx, instanceID)
	if err != nil {
		return "", nil, nil, err
	}
	var id int64
	var resolved *time.Time
	var cutoff time.Time
	err = s.pool.QueryRow(ctx, `SELECT g.id, g.resolved_at, g.cutoff_at FROM button_games g JOIN instances i ON i.id = g.instance_id
		WHERE i.public_id = $1 AND g.episode_number = $2`, toPGUUID(instanceID), episode).Scan(&id, &resolved, &cutoff)
	if err == nil {
		if resolved == nil {
			return "", nil, nil, errGameNotScored
		}
		players, err := loadButtonPlayers(ctx, s.pool, id, cutoff)
		if err != nil {
			return "", nil, nil, err
		}
		scores := buttonScores(players)
		presses := map[string]int64{}
		for _, p := range players {
			presses[p.ID] = p.Presses
		}
		ranks := buttonRanks(presses)
		out := []gameResult{}
		for _, p := range players {
			sc, r := scores[p.ID], ranks[p.ID]
			out = append(out, gameResult{Name: p.Name, Tribe: p.Tribe, TribeEmoji: emoji[p.Tribe], Points: sc.Total, Presses: p.Presses,
				StreakDays: sc.StreakDays, First: sc.First > 0, Last: sc.Last < 0,
				RankRule: r.Rule, RankPoints: r.Points, VolumePoints: pressBonus(p.Presses), StreakPoints: sc.Streak})
		}
		tribes := []tribeResult{}
		for _, t := range tribePresses(players) {
			tribes = append(tribes, tribeResult{Name: t.Name, Emoji: emoji[t.Name], Value: float64(t.Presses)})
		}
		return "press_the_button", out, tribes, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil, err
	}

	err = s.pool.QueryRow(ctx, `SELECT g.id, g.resolved_at FROM scramble_games g JOIN instances i ON i.id = g.instance_id
		WHERE i.public_id = $1 AND g.episode_number = $2`, toPGUUID(instanceID), episode).Scan(&id, &resolved)
	if err == nil {
		if resolved == nil {
			return "", nil, nil, errGameNotScored
		}
		players, err := loadScramblePlayers(ctx, s.pool, id)
		if err != nil {
			return "", nil, nil, err
		}
		places, scores := scramblePlayerPlaces(players)
		out := []gameResult{}
		for _, p := range players {
			sc := scores[p.ID]
			r := gameResult{Name: p.Name, Tribe: p.Tribe, TribeEmoji: emoji[p.Tribe], Place: places[p.ID], Points: sc.Total, WrongChecks: p.WrongChecks,
				HintPoints: sc.Hint, CleanPoints: sc.Clean}
			if p.Solved {
				r.Seconds = p.Seconds
			}
			out = append(out, r)
		}
		sort.SliceStable(out, func(i, j int) bool { // places first, in order
			a, b := out[i].Place, out[j].Place
			return a != 0 && (b == 0 || a < b)
		})
		sort.SliceStable(out, func(i, j int) bool { return out[i].Points > out[j].Points }) // most points first; ties by time
		return "spell_it_out", out, []tribeResult{}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil, err
	}

	err = s.pool.QueryRow(ctx, `SELECT g.id, g.resolved_at FROM track_games g JOIN instances i ON i.id = g.instance_id
		WHERE i.public_id = $1 AND g.episode_number = $2`, toPGUUID(instanceID), episode).Scan(&id, &resolved)
	if err == nil {
		if resolved == nil {
			return "", nil, nil, errGameNotScored
		}
		players, err := loadTrackPlayers(ctx, s.pool, id)
		if err != nil {
			return "", nil, nil, err
		}
		out := []gameResult{}
		for _, r := range trackResults(players) {
			out = append(out, gameResult{Name: r.Name, Tribe: r.Tribe, TribeEmoji: emoji[r.Tribe], Place: r.Place, Points: r.Points,
				Score: r.Score, Cups: r.Cups, BallsLeft: r.BallsLeft})
		}
		return "inside_track", out, []tribeResult{}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil, err
	}

	err = s.pool.QueryRow(ctx, `SELECT g.id, g.resolved_at FROM free_the_block_games g JOIN instances i ON i.id = g.instance_id
		WHERE i.public_id = $1 AND g.episode_number = $2`, toPGUUID(instanceID), episode).Scan(&id, &resolved)
	if err == nil {
		if resolved == nil {
			return "", nil, nil, errGameNotScored
		}
		players, err := loadFreeTheBlockPlayers(ctx, s.pool, id)
		if err != nil {
			return "", nil, nil, err
		}
		out := []gameResult{}
		for _, r := range freeTheBlockResults(players) {
			out = append(out, gameResult{Name: r.Name, Tribe: r.Tribe, TribeEmoji: emoji[r.Tribe], Place: r.Place, Points: r.Points,
				Moves: r.Moves, Solved: r.Solved})
		}
		return "free_the_block", out, []tribeResult{}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil, err
	}

	var gameID pgtype.UUID
	var answer string
	var status *string
	err = s.pool.QueryRow(ctx, `SELECT w.public_id, w.answer, o.status, w.cutoff_at FROM castawordle_games w JOIN instances i ON i.id = w.instance_id
		LEFT JOIN activity_occurrences o ON o.public_id = w.wordle_round_id
		WHERE i.public_id = $1 AND w.episode_number = $2 AND w.wordle_round_id IS NOT NULL`, toPGUUID(instanceID), episode).Scan(&gameID, &answer, &status, &cutoff)
	if err != nil {
		return "", nil, nil, err
	}
	if status == nil || *status != "resolved" {
		return "", nil, nil, errGameNotScored
	}
	results, err := s.castawordleResults(ctx, gameID, answer)
	if err != nil {
		return "", nil, nil, err
	}
	var points map[string]int
	if points, err = s.castawordlePoints(ctx, gameID); err != nil {
		return "", nil, nil, err
	}
	out := []gameResult{}
	byTribe := map[string][]int{}
	for _, r := range results {
		tribe, err := s.participantTribe(ctx, r.ID, cutoff)
		if err != nil {
			return "", nil, nil, err
		}
		out = append(out, gameResult{Name: r.Name, Tribe: tribe, TribeEmoji: emoji[tribe], Guesses: r.Guesses, Solved: r.Solved, Points: points[r.ID]})
		if tribe != "" {
			g := r.Guesses
			if !r.Solved {
				g = 7
			}
			byTribe[tribe] = append(byTribe[tribe], g)
		}
	}
	tribes := []tribeResult{}
	for name, gs := range byTribe {
		sort.Ints(gs)
		gs = gs[:min(3, len(gs))]
		sum := 0
		for _, g := range gs {
			sum += g
		}
		tribes = append(tribes, tribeResult{Name: name, Emoji: emoji[name], Value: math.Round(float64(sum)/float64(len(gs))*10) / 10})
	}
	sort.Slice(tribes, func(i, j int) bool { return tribes[i].Name < tribes[j].Name })
	return "castawordle", out, tribes, nil
}

// tribeEmoji maps the season's tribe names to their color emoji.
func (s *Server) tribeEmoji(ctx context.Context, instanceID uuid.UUID) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT g.name, COALESCE(g.color, '') FROM participant_groups g JOIN instances i ON i.id = g.instance_id
		WHERE i.public_id = $1 AND g.kind = 'tribe'`, toPGUUID(instanceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, color string
		if err := rows.Scan(&name, &color); err != nil {
			return nil, err
		}
		out[name] = tribeColorEmoji[color]
	}
	return out, rows.Err()
}

// participantTribe is a player's tribe name at a time ("" if none).
func (s *Server) participantTribe(ctx context.Context, participantID string, at time.Time) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT pg.name FROM participant_group_membership_periods m JOIN participant_groups pg ON pg.id = m.participant_group_id
		JOIN participants p ON p.id = m.participant_id
		WHERE p.public_id = $1 AND pg.kind = 'tribe' AND m.starts_at <= $2 AND (m.ends_at IS NULL OR m.ends_at > $2)
		ORDER BY m.starts_at DESC LIMIT 1`, participantID, at).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// castawordlePoints is each player's bonus from the round (individual and tribe awards).
func (s *Server) castawordlePoints(ctx context.Context, gameID pgtype.UUID) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.public_id::text, SUM(l.points)::int FROM castawordle_games w
		JOIN activity_occurrences o ON o.public_id = w.wordle_round_id
		JOIN bonus_point_ledger_entries l ON l.activity_occurrence_id = o.id
		JOIN participants p ON p.id = l.participant_id
		WHERE w.public_id = $1 GROUP BY p.public_id`, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
