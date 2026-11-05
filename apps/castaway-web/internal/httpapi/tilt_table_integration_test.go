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

// Tilt Table: the server keeps the clock (first save to the third hole) and counts aces from the rollbacks
// between sinks. Holes only fill, one per save; rollbacks only go up; a finished run is locked. Points: finishing
// +1, fastest +3, second +2 (ties in tenths share), +1 per ace for everyone.
func TestTiltTable(t *testing.T) {
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	instance := createInstanceForTest(t, ctx, q, "Tilt season", 51)
	instanceID := uuid.UUID(instance.ID.Bytes).String()
	opens := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	now := opens.Add(-time.Hour)
	users := []string{"admin", "fast", "tie", "slow", "quit", "idle"}
	ids := map[string]string{}
	for _, user := range users {
		player := createParticipantForTest(t, ctx, q, instance.ID, user)
		ids[user] = uuid.UUID(player.ID.Bytes).String()
		if _, err := q.SetParticipantDiscordUserID(ctx, db.SetParticipantDiscordUserIDParams{ID: player.ID, DiscordUserID: pgtype.Text{String: user, Valid: true}}); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(user + "-tilt"))
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
		req.AddCookie(&http.Cookie{Name: "castaway_session", Value: user + "-tilt"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, rec.Code, status, rec.Body)
		}
		return rec.Body.String()
	}
	at := func(d time.Duration) { now = opens.Add(time.Hour + d) } // everyone plays an hour after it opens

	// No game yet, and no free play until one closes.
	serve("GET", "/games/tilt-table", "", "fast", 303)
	serve("GET", "/games/tilt-table?practice=1", "", "fast", 303)
	if page := serve("GET", "/games/tilt-table?practice=1", "", "admin", 200); !strings.Contains(page, "tilt-table.js") || strings.Contains(page, "data-action") {
		t.Fatalf("admin test board: %s", page) // admins can always test, unscored
	}
	if hub := serve("GET", "/games", "", "admin", 200); !strings.Contains(hub, "Test Tilt Table") {
		t.Fatal("admin test panel missing")
	}

	create := "/api/instances/" + instanceID + "/tilt-games"
	window := fmt.Sprintf(`{"episode_number":7,"opens_at":%q,"cutoff_at":"2026-11-11T23:59:00Z"}`, opens.Format(time.RFC3339))
	serve("POST", create, window, "fast", 403)
	var game struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(serve("POST", create, window, "admin", 200)), &game); err != nil {
		t.Fatal(err)
	}
	gameURL := "/games/tilt-table/" + game.ID
	if page := serve("GET", gameURL, "", "fast", 200); !strings.Contains(page, "Opens") {
		t.Fatalf("upcoming: %s", page)
	}
	progress := func(user string, mask, rollbacks, status int) string {
		t.Helper()
		return serve("POST", gameURL+"/progress", fmt.Sprintf(`{"holes_mask":%d,"rollbacks":%d}`, mask, rollbacks), user, status)
	}
	progress("fast", 0, 0, 409) // not open yet

	at(0)
	if page := serve("GET", "/games/tilt-table", "", "fast", 200); !strings.Contains(page, `data-action="`+gameURL+`"`) || strings.Contains(page, "data-resume") {
		t.Fatalf("open game: %s", page)
	}
	if page := serve("GET", "/games/tilt-table?practice=1", "", "fast", 200); !strings.Contains(page, `data-action="`+gameURL+`"`) {
		t.Fatalf("no free play while the game is open, only the scored board: %s", page)
	}
	if hub := serve("GET", "/games", "", "fast", 200); !strings.Contains(hub, gameURL) || strings.Contains(hub, "/games/tilt-table?practice=1") {
		t.Fatal("game must be listed on /games, free play not yet")
	}

	// fast: Go, an ace, a fall, a sink after the fall (no ace), an ace to finish in 30.04s.
	progress("fast", 0, 0, 200)
	at(10 * time.Second)
	progress("fast", 1, 0, 200)
	progress("fast", 1, 1, 200)
	at(12 * time.Second)
	if page := serve("GET", gameURL, "", "fast", 200); !strings.Contains(page, `data-resume="1" data-holes="1" data-rollbacks="1" data-aces="1" data-fell="1" data-elapsed="12000"`) {
		t.Fatalf("resume: %s", page)
	}
	progress("fast", 0, 1, 409) // a hole can't empty
	progress("fast", 1, 0, 409) // rollbacks can't go down
	progress("fast", 8, 1, 400)
	progress("fast", 3, 1, 200)
	at(30*time.Second + 40*time.Millisecond)
	if res := progress("fast", 7, 1, 200); !strings.Contains(res, `"time":"0:30.0"`) || !strings.Contains(res, `"aces":2`) {
		t.Fatalf("finish: %s", res)
	}
	progress("fast", 7, 2, 409) // locked
	if page := serve("GET", gameURL, "", "fast", 200); !strings.Contains(page, "Your time: 0:30.0 · 1 rollback · 2 aces") || strings.Contains(page, "data-action") {
		t.Fatalf("played: %s", page)
	}

	// tie: three aces in 30.09s (the same tenth as fast). slow: 45s, one ace and a rollback on each of the other balls. quit: two in.
	at(0)
	progress("tie", 0, 0, 200)
	progress("tie", 2, 0, 200) // its next save (6) lost: the last one catches up two sinks, both aces
	at(30*time.Second + 90*time.Millisecond)
	progress("tie", 7, 0, 200)
	at(0)
	progress("slow", 0, 0, 200)
	progress("slow", 4, 0, 200) // ace
	progress("slow", 4, 1, 200)
	progress("slow", 5, 1, 200)
	progress("slow", 5, 2, 200)
	at(45 * time.Second)
	progress("slow", 7, 2, 200)
	progress("quit", 1, 0, 200) // its first save lost: the first sink still counts as an ace
	progress("quit", 3, 0, 200)

	now = time.Date(2026, 11, 11, 23, 58, 0, 0, time.UTC)
	serve("POST", "/api/tilt-games/"+game.ID+"/resolve", "", "admin", 409) // before cutoff
	now = time.Date(2026, 11, 11, 23, 59, 0, 0, time.UTC)
	progress("idle", 0, 0, 409) // closed
	serve("POST", "/api/tilt-games/"+game.ID+"/resolve", "", "fast", 403)
	if err := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now })).ResolveDueGames(ctx); err != nil {
		t.Fatal(err)
	}
	serve("POST", "/api/tilt-games/"+game.ID+"/resolve", "", "admin", 200) // already resolved: no second award

	// Closed: free play opens.
	if page := serve("GET", "/games/tilt-table?practice=1", "", "fast", 200); !strings.Contains(page, "tilt-table.js") || strings.Contains(page, "data-action") {
		t.Fatalf("free play after close: %s", page)
	}
	if hub := serve("GET", "/games", "", "fast", 200); !strings.Contains(hub, "/games/tilt-table?practice=1") {
		t.Fatal("free play not listed on /games after close")
	}

	if page := serve("GET", gameURL, "", "idle", 200); !strings.Contains(page, "You didn't play this one") || !strings.Contains(page, ">fast</a>") || !strings.Contains(page, "2/3 in") {
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
			Holes       int    `json:"holes"`
			Rollbacks   int    `json:"rollbacks"`
			Aces        int    `json:"aces"`
		} `json:"players"`
	}
	if err := json.Unmarshal([]byte(serve("GET", "/api/instances/"+instanceID+"/game-results/7", "", "admin", 200)), &res); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range res.Players {
		got = append(got, fmt.Sprintf("%s %d %d %d %t %d %d %d %d", p.Name, p.Place, p.Points, p.PlacePoints, p.Finished, p.TimeMS, p.Holes, p.Rollbacks, p.Aces))
	}
	want := "fast 1 6 3 true 30040 3 1 2|tie 1 7 3 true 30090 3 0 3|slow 2 4 2 true 45000 3 2 1|quit 0 2 0 false 0 2 0 2"
	if got := strings.Join(got, "|"); res.Type != "tilt_table" || got != want {
		t.Errorf("game results: %s\n got %s\nwant %s", res.Type, got, want)
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
	for user, want := range map[string]int{"fast": 6, "tie": 7, "slow": 4, "quit": 2, "idle": 0, "admin": 0} {
		if totals[ids[user]] != want {
			t.Errorf("%s earned %d, want %d", user, totals[ids[user]], want)
		}
	}
	var awardRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bonus_point_ledger_entries WHERE award_key = 'tilt_table'`).Scan(&awardRows); err != nil || awardRows != 4 {
		t.Fatalf("tilt_table ledger rows %d, err %v; want 4", awardRows, err)
	}
	if page := serve("GET", "/players/"+ids["fast"], "", "idle", 200); !strings.Contains(page, "Tilt Table") || !strings.Contains(page, "0:30.0 · 2 aces · &#43;6 pts") {
		t.Fatalf("profile: %s", page)
	}
	if page := serve("GET", "/players/"+ids["quit"], "", "idle", 200); !strings.Contains(page, "2/3 in · 2 aces · &#43;2 pts") {
		t.Fatalf("quit profile: %s", page)
	}
	if list := serve("GET", "/api/instances/"+instanceID+"/scheduled-games", "", "admin", 200); !strings.Contains(list, "tilt_table") {
		t.Fatalf("scheduled games: %s", list)
	}
}
