package httpapi_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/httpapi"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Inside Track: one run per player, progress only moves forward, a reload resumes the run, and at cutoff
// each player earns their score (+1 per cup, +1 per ball left once all three are in), once.
func TestInsideTrack(t *testing.T) {
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	instance := createInstanceForTest(t, ctx, q, "Track season", 51)
	instanceID := uuid.UUID(instance.ID.Bytes).String()
	now := time.Date(2026, 10, 21, 23, 0, 0, 0, time.UTC)
	users := []string{"admin", "ace", "tieA", "tieB", "low", "quit", "idle"}
	ids := map[string]string{}
	for _, user := range users {
		player := createParticipantForTest(t, ctx, q, instance.ID, user)
		ids[user] = uuid.UUID(player.ID.Bytes).String()
		if _, err := q.SetParticipantDiscordUserID(ctx, db.SetParticipantDiscordUserIDParams{ID: player.ID, DiscordUserID: pgtype.Text{String: user, Valid: true}}); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(user + "-track"))
		if _, err := pool.Exec(ctx, `INSERT INTO web_sessions (token_hash, discord_user_id, discord_username, kind, expires_at) VALUES ($1,$2,$2,'browser',$3)`, hash[:], user, now.AddDate(0, 1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.CreateInstanceAdmin(ctx, db.CreateInstanceAdminParams{InstanceID: instance.ID, DiscordUserID: "admin"}); err != nil {
		t.Fatal(err)
	}
	const base = "https://castaway.example"
	router := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now }), httpapi.WithPublic(httpapi.PublicConfig{BaseURL: base, InstanceID: instanceID})).PublicRouter()
	serve := func(method, path, body, user string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		req.AddCookie(&http.Cookie{Name: "castaway_session", Value: user + "-track"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, rec.Code, status, rec.Body)
		}
		return rec.Body.String()
	}
	progress := func(_ string, mask, used int, finished bool) string { // first arg names the player, for reading
		return fmt.Sprintf(`{"cups_mask":%d,"balls_used":%d,"finished":%t}`, mask, used, finished)
	}

	// No game yet: admins get the practice board, players go back to /games.
	if page := serve("GET", "/games/inside-track", "", "admin", 200); !strings.Contains(page, "inside-track.js") || strings.Contains(page, "data-action") {
		t.Fatalf("admin practice board: %s", page)
	}
	serve("GET", "/games/inside-track", "", "ace", 302)

	create := "/api/instances/" + instanceID + "/track-games"
	window := `{"episode_number":5,"opens_at":"2026-10-22T00:00:00Z","cutoff_at":"2026-10-28T23:59:00Z"}`
	serve("POST", create, window, "ace", 403)
	var game struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(serve("POST", create, window, "admin", 200)), &game); err != nil {
		t.Fatal(err)
	}
	serve("POST", create, window, "admin", 200) // re-apply before anyone plays is fine
	gameURL := "/games/inside-track/" + game.ID
	if page := serve("GET", gameURL, "", "ace", 200); !strings.Contains(page, "Opens") {
		t.Fatalf("upcoming: %s", page)
	}
	serve("POST", gameURL+"/progress", progress("ace", 0, 1, false), "ace", 409)

	now = time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	if page := serve("GET", "/games/inside-track", "", "ace", 200); !strings.Contains(page, `data-action="`+gameURL+`"`) || strings.Contains(page, "data-resume") {
		t.Fatalf("open game: %s", page)
	}
	if page := serve("GET", "/games/inside-track?practice=1", "", "admin", 200); strings.Contains(page, "data-action") {
		t.Fatalf("admin practice while open must not be scored: %s", page)
	}
	if page := serve("GET", "/games/inside-track?practice=1", "", "ace", 200); strings.Contains(page, "data-action") {
		t.Fatalf("free play is unscored for players too: %s", page)
	}
	if !strings.Contains(serve("GET", "/games", "", "ace", 200), gameURL) {
		t.Fatal("game not listed on /games")
	}
	// ace: all three cups with 5 balls → 3 + 7 = 10.
	serve("POST", gameURL+"/progress", progress("ace", 0, 1, false), "ace", 200)
	serve("POST", gameURL+"/progress", progress("ace", 1, 1, false), "ace", 200)
	serve("POST", gameURL+"/progress", progress("ace", 1, 4, false), "ace", 200)
	if page := serve("GET", gameURL, "", "ace", 200); !strings.Contains(page, `data-cups="1" data-used="4" data-resume="1"`) {
		t.Fatalf("resume: %s", page)
	}
	serve("POST", gameURL+"/progress", progress("ace", 0, 5, false), "ace", 409) // a cup can't empty
	serve("POST", gameURL+"/progress", progress("ace", 1, 3, false), "ace", 409) // balls can't come back
	serve("POST", gameURL+"/progress", progress("ace", 3, 9, true), "ace", 400)  // finished needs 3 cups or 12 balls
	serve("POST", gameURL+"/progress", progress("ace", 7, 2, false), "ace", 400) // more cups than balls
	serve("POST", gameURL+"/progress", progress("ace", 7, 5, true), "ace", 200)
	serve("POST", gameURL+"/progress", progress("ace", 7, 6, true), "ace", 409) // one run
	if page := serve("GET", gameURL, "", "ace", 200); !strings.Contains(page, "Your score: 10") || strings.Contains(page, "data-action") {
		t.Fatalf("played: %s", page)
	}
	// tieA and tieB: two cups after all 12 → 2 each. low: one cup → 1. quit: left after 3 balls, no cups → 0.
	for _, user := range []string{"tieA", "tieB"} {
		serve("POST", gameURL+"/progress", progress(user, 5, 12, true), user, 200)
	}
	serve("POST", gameURL+"/progress", progress("low", 2, 12, true), "low", 200)
	serve("POST", gameURL+"/progress", progress("quit", 0, 3, false), "quit", 200)

	now = time.Date(2026, 10, 28, 23, 58, 0, 0, time.UTC)
	serve("POST", "/api/track-games/"+game.ID+"/resolve", "", "admin", 409) // before cutoff
	now = time.Date(2026, 10, 28, 23, 59, 0, 0, time.UTC)
	serve("POST", gameURL+"/progress", progress("idle", 0, 1, false), "idle", 409) // closed
	serve("POST", "/api/track-games/"+game.ID+"/resolve", "", "ace", 403)
	if err := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now })).ResolveDueGames(ctx); err != nil {
		t.Fatal(err)
	}
	serve("POST", "/api/track-games/"+game.ID+"/resolve", "", "admin", 200) // already resolved: no second award

	page := serve("GET", gameURL, "", "idle", 200)
	if !strings.Contains(page, "You didn't play this one") || !strings.Contains(page, ">ace</a>") {
		t.Fatalf("results: %s", page)
	}
	var res struct {
		Type    string `json:"type"`
		Players []struct {
			Name      string `json:"name"`
			Place     int    `json:"place"`
			Points    int    `json:"points"`
			Score     int    `json:"score"`
			Cups      int    `json:"cups"`
			BallsLeft int    `json:"balls_left"`
		} `json:"players"`
	}
	if err := json.Unmarshal([]byte(serve("GET", "/api/instances/"+instanceID+"/game-results/5", "", "admin", 200)), &res); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(res.Type, res.Players); got != "inside_track[{ace 1 10 10 3 7} {tieA 2 2 2 2 0} {tieB 2 2 2 2 0} {low 4 1 1 1 0} {quit 5 0 0 0 0}]" {
		t.Errorf("game results: %s", got)
	}
	totals := map[string]int{}
	rows, err := pool.Query(ctx, `SELECT p.public_id::text, COALESCE(SUM(l.points),0) FROM participants p LEFT JOIN bonus_point_ledger_entries l ON l.participant_id = p.id GROUP BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var points int
		if err := rows.Scan(&id, &points); err != nil {
			t.Fatal(err)
		}
		totals[id] = points
	}
	rows.Close()
	for user, want := range map[string]int{"ace": 10, "tieA": 2, "tieB": 2, "low": 1, "quit": 0, "idle": 0, "admin": 0} {
		if totals[ids[user]] != want {
			t.Errorf("%s earned %d, want %d", user, totals[ids[user]], want)
		}
	}
	if page := serve("GET", "/players/"+ids["ace"], "", "idle", 200); !strings.Contains(page, "Inside Track") || !strings.Contains(page, "10 in-game · &#43;10 pts") {
		t.Fatalf("profile: %s", page)
	}
}

// Admin corrections: admins list every run before a game resolves and can set a run (even backwards, and not
// after it resolves); players can't; the write touches only the play row.
func TestInsideTrackAdminPlays(t *testing.T) {
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	instance := createInstanceForTest(t, ctx, q, "Track admin", 52)
	instanceID := uuid.UUID(instance.ID.Bytes).String()
	now := time.Date(2026, 10, 21, 23, 0, 0, 0, time.UTC)
	ids := map[string]string{}
	for _, user := range []string{"admin", "ace", "bo"} {
		player := createParticipantForTest(t, ctx, q, instance.ID, user)
		ids[user] = uuid.UUID(player.ID.Bytes).String()
		if _, err := q.SetParticipantDiscordUserID(ctx, db.SetParticipantDiscordUserIDParams{ID: player.ID, DiscordUserID: pgtype.Text{String: user, Valid: true}}); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(user + "-track-admin"))
		if _, err := pool.Exec(ctx, `INSERT INTO web_sessions (token_hash, discord_user_id, discord_username, kind, expires_at) VALUES ($1,$2,$2,'browser',$3)`, hash[:], user, now.AddDate(0, 1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.CreateInstanceAdmin(ctx, db.CreateInstanceAdminParams{InstanceID: instance.ID, DiscordUserID: "admin"}); err != nil {
		t.Fatal(err)
	}
	const base = "https://castaway.example"
	router := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now }), httpapi.WithPublic(httpapi.PublicConfig{BaseURL: base, InstanceID: instanceID})).PublicRouter()
	serve := func(method, path, body, user string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		req.AddCookie(&http.Cookie{Name: "castaway_session", Value: user + "-track-admin"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, rec.Code, status, rec.Body)
		}
		return rec.Body.String()
	}
	var created struct {
		ID string `json:"id"`
	}
	create := "/api/instances/" + instanceID + "/track-games"
	if err := json.Unmarshal([]byte(serve("POST", create, `{"episode_number":6,"opens_at":"2026-10-22T00:00:00Z","cutoff_at":"2026-10-28T23:59:00Z"}`, "admin", 200)), &created); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	plays := "/api/instances/" + instanceID + "/track-games/6/plays"
	play := func(user string) string { return plays + "/" + ids[user] }
	setBody := func(mask, used int, finished bool) string {
		return fmt.Sprintf(`{"cups_mask":%d,"balls_used":%d,"finished":%t}`, mask, used, finished)
	}
	var list struct {
		GameID   string `json:"game_id"`
		Open     bool   `json:"open"`
		Resolved bool   `json:"resolved"`
		Plays    []struct {
			Name      string `json:"name"`
			Cups      int    `json:"cups"`
			BallsUsed int    `json:"balls_used"`
			Finished  bool   `json:"finished"`
			Score     int    `json:"score"`
			Place     int    `json:"place"`
			Points    int    `json:"points"`
		} `json:"plays"`
	}
	decode := func(body string) {
		list.Plays = nil
		if err := json.Unmarshal([]byte(body), &list); err != nil {
			t.Fatal(err)
		}
	}
	const bothRuns = "bo place 1 score 3 cups 3 balls 12 finished true points 3; ace place 2 score 1 cups 1 balls 2 finished false points 1"
	summary := func() string {
		var out []string
		for _, p := range list.Plays {
			out = append(out, fmt.Sprintf("%s place %d score %d cups %d balls %d finished %t points %d", p.Name, p.Place, p.Score, p.Cups, p.BallsUsed, p.Finished, p.Points))
		}
		return strings.Join(out, "; ")
	}

	serve("GET", plays, "", "ace", 403)
	serve("PUT", play("ace"), setBody(1, 1, false), "ace", 403)
	serve("GET", "/api/instances/"+instanceID+"/track-games/9/plays", "", "admin", 404)
	serve("PUT", "/api/instances/"+instanceID+"/track-games/9/plays/"+ids["ace"], setBody(1, 1, false), "admin", 404)
	serve("PUT", "/api/instances/"+instanceID+"/track-games/6/plays/"+uuid.NewString(), setBody(1, 1, false), "admin", 404)
	serve("PUT", play("ace"), setBody(0, 13, false), "admin", 400)
	serve("PUT", play("ace"), setBody(8, 12, false), "admin", 400)
	serve("PUT", play("ace"), setBody(7, 2, false), "admin", 400) // more cups than balls
	serve("PUT", play("ace"), setBody(3, 9, true), "admin", 400)  // finished needs 3 cups or 12 balls
	decode(serve("GET", plays, "", "admin", 200))
	if list.GameID != created.ID || !list.Open || list.Resolved || len(list.Plays) != 0 {
		t.Fatalf("empty list: %+v", list)
	}

	// Corrections go up and down; the last one wins, and the list is ordered by place.
	serve("PUT", play("ace"), setBody(7, 5, true), "admin", 200)
	serve("PUT", play("ace"), setBody(1, 2, false), "admin", 200) // backwards is allowed for a correction
	serve("PUT", play("bo"), setBody(7, 12, true), "admin", 200)
	decode(serve("GET", plays, "", "admin", 200))
	if got := summary(); got != bothRuns {
		t.Fatalf("plays: %s", got)
	}

	// Nothing but the play row changes: no ledger entries, and the game is still unresolved.
	var ledger int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bonus_point_ledger_entries`).Scan(&ledger); err != nil || ledger != 0 {
		t.Fatalf("ledger rows %d, err %v; want none", ledger, err)
	}

	now = time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	serve("POST", "/api/track-games/"+created.ID+"/resolve", "", "admin", 200)
	serve("PUT", play("ace"), setBody(7, 12, true), "admin", 409)
	decode(serve("GET", plays, "", "admin", 200))
	if !list.Resolved || list.Open || summary() != bothRuns {
		t.Fatalf("after resolve: %+v %s", list, summary())
	}
}
