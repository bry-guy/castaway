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

// Stack 'Em Up: the server keeps the clock (first save to the held tower). Drops only go up; without a new drop
// the tower only grows; a finished run is locked. Points: finishing +1, fastest +3, second +2 (ties in tenths
// share).
func TestStackEmUp(t *testing.T) {
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	instance := createInstanceForTest(t, ctx, q, "Stack season", 51)
	instanceID := uuid.UUID(instance.ID.Bytes).String()
	opens := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	now := opens.Add(-time.Hour)
	users := []string{"admin", "fast", "tie", "slow", "quit", "idle"}
	ids := map[string]string{}
	for _, user := range users {
		player := createParticipantForTest(t, ctx, q, instance.ID, user)
		ids[user] = uuid.UUID(player.ID.Bytes).String()
		if _, err := q.SetParticipantDiscordUserID(ctx, db.SetParticipantDiscordUserIDParams{ID: player.ID, DiscordUserID: pgtype.Text{String: user, Valid: true}}); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(user + "-stack"))
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
		req.AddCookie(&http.Cookie{Name: "castaway_session", Value: user + "-stack"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, rec.Code, status, rec.Body)
		}
		return rec.Body.String()
	}
	at := func(d time.Duration) { now = opens.Add(time.Hour + d) } // everyone plays an hour after it opens

	// No game yet, and no free play until one closes.
	serve("GET", "/games/stack-em-up", "", "fast", 303)
	serve("GET", "/games/stack-em-up?practice=1", "", "fast", 303)
	if page := serve("GET", "/games/stack-em-up?practice=1", "", "admin", 200); !strings.Contains(page, "stack-em-up.js") || strings.Contains(page, "data-action") {
		t.Fatalf("admin test board: %s", page) // admins can always test, unscored
	}
	if hub := serve("GET", "/games", "", "admin", 200); !strings.Contains(hub, "Test Stack 'Em Up") {
		t.Fatal("admin test panel missing")
	}

	create := "/api/instances/" + instanceID + "/stack-games"
	window := fmt.Sprintf(`{"episode_number":8,"opens_at":%q,"cutoff_at":"2026-11-18T23:59:00Z"}`, opens.Format(time.RFC3339))
	serve("POST", create, window, "fast", 403)
	var game struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(serve("POST", create, window, "admin", 200)), &game); err != nil {
		t.Fatal(err)
	}
	gameURL := "/games/stack-em-up/" + game.ID
	if page := serve("GET", gameURL, "", "fast", 200); !strings.Contains(page, "Opens") {
		t.Fatalf("upcoming: %s", page)
	}
	progress := func(user string, height, drops, status int) string {
		t.Helper()
		return serve("POST", gameURL+"/progress", fmt.Sprintf(`{"height":%d,"drops":%d}`, height, drops), user, status)
	}
	progress("fast", 0, 0, 409) // not open yet

	at(0)
	if page := serve("GET", "/games/stack-em-up", "", "fast", 200); !strings.Contains(page, `data-action="`+gameURL+`"`) || strings.Contains(page, "data-resume") {
		t.Fatalf("open game: %s", page)
	}
	if page := serve("GET", "/games/stack-em-up?practice=1", "", "fast", 200); !strings.Contains(page, `data-action="`+gameURL+`"`) {
		t.Fatalf("no free play while the game is open, only the scored game: %s", page)
	}
	if hub := serve("GET", "/games", "", "fast", 200); !strings.Contains(hub, gameURL) || strings.Contains(hub, "/games/stack-em-up?practice=1") {
		t.Fatal("game must be listed on /games, free play not yet")
	}

	// fast: Go, three blocks, a drop, then the full tower held at 30.04s.
	progress("fast", 0, 0, 200)
	progress("fast", 3, 0, 200)
	progress("fast", 0, 1, 200)
	at(12 * time.Second)
	if page := serve("GET", gameURL, "", "fast", 200); !strings.Contains(page, `data-resume="1" data-height="0" data-drops="1" data-elapsed="12000"`) {
		t.Fatalf("resume: %s", page)
	}
	progress("fast", 4, 0, 409) // drops can't go down
	progress("fast", 4, 1, 200)
	progress("fast", 3, 1, 409) // the tower can't shrink without a drop
	progress("fast", 9, 1, 400)
	at(30*time.Second + 40*time.Millisecond)
	if res := progress("fast", 8, 1, 200); !strings.Contains(res, `"time":"0:30.0"`) {
		t.Fatalf("finish: %s", res)
	}
	progress("fast", 8, 2, 409) // locked
	if page := serve("GET", gameURL, "", "fast", 200); !strings.Contains(page, "Your time: 0:30.0 · 1 drop") || strings.Contains(page, "data-action") {
		t.Fatalf("played: %s", page)
	}

	// tie: clean in 30.09s (the same tenth as fast). slow: 45s with two drops. quit: five high, then a drop.
	at(0)
	progress("tie", 0, 0, 200)
	progress("tie", 5, 0, 200) // saves in between lost: the tower still only grew
	at(30*time.Second + 90*time.Millisecond)
	progress("tie", 8, 0, 200)
	at(0)
	progress("slow", 0, 0, 200)
	progress("slow", 2, 1, 200)
	progress("slow", 0, 2, 200)
	at(45 * time.Second)
	progress("slow", 8, 2, 200)
	progress("quit", 5, 0, 200)
	progress("quit", 0, 1, 200)

	now = time.Date(2026, 11, 18, 23, 58, 0, 0, time.UTC)
	serve("POST", "/api/stack-games/"+game.ID+"/resolve", "", "admin", 409) // before cutoff
	now = time.Date(2026, 11, 18, 23, 59, 0, 0, time.UTC)
	progress("idle", 0, 0, 409) // closed
	serve("POST", "/api/stack-games/"+game.ID+"/resolve", "", "fast", 403)
	if err := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now })).ResolveDueGames(ctx); err != nil {
		t.Fatal(err)
	}
	serve("POST", "/api/stack-games/"+game.ID+"/resolve", "", "admin", 200) // already resolved: no second award

	// Closed: free play opens.
	if page := serve("GET", "/games/stack-em-up?practice=1", "", "fast", 200); !strings.Contains(page, "stack-em-up.js") || strings.Contains(page, "data-action") {
		t.Fatalf("free play after close: %s", page)
	}
	if hub := serve("GET", "/games", "", "fast", 200); !strings.Contains(hub, "/games/stack-em-up?practice=1") {
		t.Fatal("free play not listed on /games after close")
	}

	if page := serve("GET", gameURL, "", "idle", 200); !strings.Contains(page, "You didn't play this one") || !strings.Contains(page, ">fast</a>") || !strings.Contains(page, "5/8 stacked") {
		t.Fatalf("results: %s", page)
	}
	var res struct {
		Type    string `json:"type"`
		Players []struct {
			Name        string `json:"name"`
			Place       int    `json:"place"`
			Points      int    `json:"points"`
			PlacePoints int    `json:"place_points"`
			Finished    bool   `json:"finished"`
			TimeMS      int64  `json:"time_ms"`
			Height      int    `json:"height"`
			Drops       int    `json:"drops"`
		} `json:"players"`
	}
	if err := json.Unmarshal([]byte(serve("GET", "/api/instances/"+instanceID+"/game-results/8", "", "admin", 200)), &res); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range res.Players {
		got = append(got, fmt.Sprintf("%s %d %d %d %t %d %d %d", p.Name, p.Place, p.Points, p.PlacePoints, p.Finished, p.TimeMS, p.Height, p.Drops))
	}
	want := "fast 1 4 3 true 30040 8 1|tie 1 4 3 true 30090 8 0|slow 2 3 2 true 45000 8 2|quit 0 0 0 false 0 5 1"
	if got := strings.Join(got, "|"); res.Type != "stack_em_up" || got != want {
		t.Errorf("game results: %s\n got %s\nwant %s", res.Type, got, want)
	}
	var awardRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bonus_point_ledger_entries WHERE award_key = 'stack_em_up'`).Scan(&awardRows); err != nil || awardRows != 3 {
		t.Fatalf("stack_em_up ledger rows %d, err %v; want 3", awardRows, err)
	}
	if page := serve("GET", "/players/"+ids["fast"], "", "idle", 200); !strings.Contains(page, "Stack &#39;Em Up") || !strings.Contains(page, "0:30.0 · 1 drop · &#43;4 pts") {
		t.Fatalf("profile: %s", page)
	}
	if page := serve("GET", "/players/"+ids["quit"], "", "idle", 200); !strings.Contains(page, "5/8 stacked · 1 drop · &#43;0 pts") {
		t.Fatalf("quit profile: %s", page)
	}
	if list := serve("GET", "/api/instances/"+instanceID+"/scheduled-games", "", "admin", 200); !strings.Contains(list, "stack_em_up") {
		t.Fatalf("scheduled games: %s", list)
	}
}
