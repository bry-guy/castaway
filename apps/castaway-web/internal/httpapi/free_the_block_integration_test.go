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
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/freetheblock"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/httpapi"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Free the Block: the server replays every move list from the start. Illegal moves and lists that don't extend the
// saved run are refused; a solve scores by the moves it took (fewest +2, second +1, ties share), once.
func TestFreeTheBlock(t *testing.T) {
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	instance := createInstanceForTest(t, ctx, q, "Block season", 51)
	instanceID := uuid.UUID(instance.ID.Bytes).String()
	now := time.Date(2026, 10, 21, 23, 0, 0, 0, time.UTC)
	users := []string{"admin", "ace", "tieA", "low", "quit", "idle"}
	ids := map[string]string{}
	for _, user := range users {
		player := createParticipantForTest(t, ctx, q, instance.ID, user)
		ids[user] = uuid.UUID(player.ID.Bytes).String()
		if _, err := q.SetParticipantDiscordUserID(ctx, db.SetParticipantDiscordUserIDParams{ID: player.ID, DiscordUserID: pgtype.Text{String: user, Valid: true}}); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(user + "-block"))
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
		req.AddCookie(&http.Cookie{Name: "castaway_session", Value: user + "-block"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, rec.Code, status, rec.Body)
		}
		return rec.Body.String()
	}
	progress := func(moves []freetheblock.Move, finished bool) string {
		body, err := json.Marshal(map[string]any{"moves": moves, "finished": finished})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	solution, err := freetheblock.Solve()
	if err != nil {
		t.Fatal(err)
	}
	if res, err := freetheblock.Replay(solution); err != nil || !res.Solved || res.Moves != 81 {
		t.Fatalf("BFS solution: %+v, %v; want solved in 81 moves", res, err)
	}
	// low opens with a slide of block 8 and its return: the same position, one move more in the count.
	padded := append([]freetheblock.Move{{Piece: 8, Dir: "right", Steps: 1}, {Piece: 8, Dir: "left", Steps: 1}}, solution...)
	if res, err := freetheblock.Replay(padded); err != nil || !res.Solved || res.Moves != 82 {
		t.Fatalf("padded solution: %+v, %v; want solved in 82 moves", res, err)
	}

	// No game yet: admins get the practice board, players go back to /games.
	if page := serve("GET", "/games/free-the-block", "", "admin", 200); !strings.Contains(page, "free-the-block.js") || strings.Contains(page, "data-action") {
		t.Fatalf("admin practice board: %s", page)
	}
	serve("GET", "/games/free-the-block", "", "ace", 302)

	create := "/api/instances/" + instanceID + "/free-the-block-games"
	window := `{"episode_number":5,"opens_at":"2026-10-22T00:00:00Z","cutoff_at":"2026-10-28T23:59:00Z"}`
	serve("POST", create, window, "ace", 403)
	var game struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(serve("POST", create, window, "admin", 200)), &game); err != nil {
		t.Fatal(err)
	}
	gameURL := "/games/free-the-block/" + game.ID
	if page := serve("GET", gameURL, "", "ace", 200); !strings.Contains(page, "Opens") {
		t.Fatalf("upcoming: %s", page)
	}
	serve("POST", gameURL+"/progress", progress(solution[:1], false), "ace", 409)

	now = time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	if page := serve("GET", "/games/free-the-block", "", "ace", 200); !strings.Contains(page, `data-action="`+gameURL+`"`) || strings.Contains(page, "data-moves") {
		t.Fatalf("open game: %s", page)
	}
	if page := serve("GET", "/games/free-the-block?practice=1", "", "admin", 200); strings.Contains(page, "data-action") {
		t.Fatalf("admin practice while open must not be scored: %s", page)
	}
	if !strings.Contains(serve("GET", "/games", "", "ace", 200), gameURL) {
		t.Fatal("game not listed on /games")
	}

	// ace: a legal start, then refusals. Block 6 (a soldier) can't go up into the horizontal; block 10 doesn't exist.
	serve("POST", gameURL+"/progress", progress(solution[:4], false), "ace", 200)
	serve("POST", gameURL+"/progress", progress(solution[:4], false), "ace", 200) // the same list again is fine
	serve("POST", gameURL+"/progress", progress([]freetheblock.Move{{Piece: 6, Dir: "up", Steps: 1}}, false), "ace", 400)
	serve("POST", gameURL+"/progress", progress([]freetheblock.Move{{Piece: 10, Dir: "left", Steps: 1}}, false), "ace", 400)
	serve("POST", gameURL+"/progress", progress(solution[:2], false), "ace", 409)                                           // shorter than the saved run
	serve("POST", gameURL+"/progress", progress([]freetheblock.Move{{Piece: 9, Dir: "left", Steps: 1}}, false), "ace", 409) // legal, but not the saved run
	serve("POST", gameURL+"/progress", progress(solution[:6], true), "ace", 400)                                            // not out yet, can't finish
	if page := serve("GET", gameURL, "", "ace", 200); !strings.Contains(page, `data-moves="`+freetheblock.Encode(solution[:4])+`"`) {
		t.Fatalf("resume: %s", page)
	}
	serve("POST", gameURL+"/progress", progress(solution, true), "ace", 200)
	serve("POST", gameURL+"/progress", progress(append(append([]freetheblock.Move(nil), solution...), freetheblock.Move{Piece: 9, Dir: "left", Steps: 1}), false), "ace", 400) // after the idol is out
	serve("POST", gameURL+"/progress", progress(solution, true), "ace", 409)                                                                                                   // one run
	if page := serve("GET", gameURL, "", "ace", 200); !strings.Contains(page, "Solved in 81 moves") || strings.Contains(page, "data-action") {
		t.Fatalf("played: %s", page)
	}

	// tieA ties ace at 81; low solves in 82; quit stops short of solving; idle never starts.
	serve("POST", gameURL+"/progress", progress(solution, true), "tieA", 200)
	serve("POST", gameURL+"/progress", progress(padded, true), "low", 200)
	serve("POST", gameURL+"/progress", progress(solution[:20], false), "quit", 200)

	now = time.Date(2026, 10, 28, 23, 58, 0, 0, time.UTC)
	serve("POST", "/api/free-the-block-games/"+game.ID+"/resolve", "", "admin", 409) // before cutoff
	now = time.Date(2026, 10, 28, 23, 59, 0, 0, time.UTC)
	serve("POST", gameURL+"/progress", progress(solution[:1], false), "idle", 409) // closed
	serve("POST", "/api/free-the-block-games/"+game.ID+"/resolve", "", "ace", 403)
	if err := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now })).ResolveDueGames(ctx); err != nil {
		t.Fatal(err)
	}
	serve("POST", "/api/free-the-block-games/"+game.ID+"/resolve", "", "admin", 200) // already resolved: no second award

	if page := serve("GET", gameURL, "", "idle", 200); !strings.Contains(page, "You didn't play this one") || !strings.Contains(page, ">ace</a>") {
		t.Fatalf("results: %s", page)
	}
	var res struct {
		Type    string `json:"type"`
		Players []struct {
			Name   string `json:"name"`
			Place  int    `json:"place"`
			Points int    `json:"points"`
			Moves  int    `json:"moves"`
			Solved bool   `json:"solved"`
		} `json:"players"`
	}
	if err := json.Unmarshal([]byte(serve("GET", "/api/instances/"+instanceID+"/game-results/5", "", "admin", 200)), &res); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range res.Players {
		got = append(got, fmt.Sprintf("%s %d %d %t", p.Name, p.Place, p.Points, p.Solved))
		if p.Solved && p.Moves != map[string]int{"ace": 81, "tieA": 81, "low": 82}[p.Name] {
			t.Errorf("%s moves %d", p.Name, p.Moves)
		}
	}
	if got := strings.Join(got, "|"); res.Type != "free_the_block" || got != "ace 1 3 true|tieA 1 3 true|low 3 1 true|quit 0 0 false" {
		t.Errorf("game results: %s %s", res.Type, got)
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
	for user, want := range map[string]int{"ace": 3, "tieA": 3, "low": 1, "quit": 0, "idle": 0, "admin": 0} {
		if totals[ids[user]] != want {
			t.Errorf("%s earned %d, want %d", user, totals[ids[user]], want)
		}
	}
	var awardKeys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bonus_point_ledger_entries WHERE award_key = 'free_the_block'`).Scan(&awardKeys); err != nil || awardKeys != 3 {
		t.Fatalf("free_the_block ledger rows %d, err %v; want 3", awardKeys, err)
	}
	if page := serve("GET", "/players/"+ids["ace"], "", "idle", 200); !strings.Contains(page, "Free the Block") || !strings.Contains(page, "Solved in 81 moves · &#43;3 pts") {
		t.Fatalf("profile: %s", page)
	}
	if page := serve("GET", "/players/"+ids["quit"], "", "idle", 200); !strings.Contains(page, "Not solved · &#43;0 pts") {
		t.Fatalf("quit profile: %s", page)
	}
	if list := serve("GET", "/api/instances/"+instanceID+"/scheduled-games", "", "admin", 200); !strings.Contains(list, "free_the_block") {
		t.Fatalf("scheduled games: %s", list)
	}
}
