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
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Pick Your Champion: picks change until the lock, then are fixed; only challenges after the lock award the
// winners' pickers (+3 immunity, +2 reward); a voted-out Champion can't be picked and stops scoring.
func TestChampionPicks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	instance := createInstanceForTest(t, ctx, q, "Champion", 960)
	other := createInstanceForTest(t, ctx, q, "Champion other", 961)
	id := func(u [16]byte) string { return uuid.UUID(u).String() }
	players := map[string]string{"Alice": "1001", "Bob": "1002", "Cat": "1003"}
	for name, discord := range players {
		p := createParticipantForTest(t, ctx, q, instance.ID, name)
		if _, err := pool.Exec(ctx, `UPDATE participants SET discord_user_id = $2 WHERE public_id = $1`, p.ID, discord); err != nil {
			t.Fatal(err)
		}
	}
	x := createContestantForTest(t, ctx, q, instance.ID, "Xena Warrior")
	y := createContestantForTest(t, ctx, q, instance.ID, "Yuri Gagarin")
	z := createContestantForTest(t, ctx, q, instance.ID, "Zed Zeppelin")
	for _, inst := range []db.CreateInstanceRow{instance, other} {
		if _, err := q.CreateInstanceAdmin(ctx, db.CreateInstanceAdminParams{InstanceID: inst.ID, DiscordUserID: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	for ch, inst := range map[string]db.CreateInstanceRow{"7002": instance, "7003": other} {
		if err := q.SetDiscordChannelBinding(ctx, db.SetDiscordChannelBindingParams{GuildID: "7001", ChannelID: ch, InstanceID: inst.ID}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2030, 11, 1, 12, 0, 0, 0, time.UTC)
	router := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now }), httpapi.WithServiceAuth(httpapi.ServiceAuthConfig{Enabled: true, BearerTokens: []string{"token"}})).Router()
	path := "/instances/" + id(instance.ID.Bytes)
	call := func(method, url, body, actor string, want int) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, authorizedJSONRequest(method, url, body, "token", actor))
		if rec.Code != want {
			t.Fatalf("%s %s %s: %d %s", method, url, body, rec.Code, rec.Body.String())
		}
		out := map[string]any{}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	pick := func(discord, name string, want int) map[string]any {
		return call("PUT", "/discord/guilds/7001/channels/7002/champion-pick", fmt.Sprintf(`{"discord_user_id":%q,"contestant":%q}`, discord, name), "", want)
	}
	bonus := func() map[string]float64 {
		var res struct {
			Leaderboard []struct {
				Name  string  `json:"participant_name"`
				Bonus float64 `json:"bonus_points"`
			} `json:"leaderboard"`
		}
		raw, err := json.Marshal(call("GET", path+"/leaderboard", "", "admin", 200))
		if err != nil || json.Unmarshal(raw, &res) != nil {
			t.Fatal("leaderboard decode", err)
		}
		got := map[string]float64{}
		for _, r := range res.Leaderboard {
			got[r.Name] = r.Bonus
		}
		return got
	}
	imp := func(ep int, boots, champs string, want int) map[string]any {
		return call("POST", path+"/episode-imports", fmt.Sprintf(`{"episode_number":%d,"source":"survivor","source_revision":"r","boots":[%s],"challenges":[],"champion_challenges":[%s]}`, ep, boots, champs), "admin", want)
	}
	champ := func(key, kind string, at time.Time, winners ...string) string {
		w, err := json.Marshal(winners)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf(`{"key":%q,"kind":%q,"winners":%s,"effective_at":%q}`, key, kind, w, at.Format(time.RFC3339))
	}

	pick("1001", "Xena", 409) // no round yet
	opens, locks := now, now.Add(7*24*time.Hour)
	round := fmt.Sprintf(`{"merge_episode":7,"opens_at":%q,"locks_at":%q}`, opens.Format(time.RFC3339), locks.Format(time.RFC3339))
	call("PUT", path+"/champion-round", round, "nobody", 403)
	call("PUT", path+"/champion-round", round, "admin", 200)

	// Merge-episode challenge before the lock: recorded, but nobody's Champion is locked yet.
	pick("1001", "Xena", 200)
	if res := imp(7, "", champ("survivor-ep7-c1-immunity", "immunity", now.Add(time.Hour), id(x.ID.Bytes)), 201); res["challenge_awards"] != 0.0 {
		t.Fatalf("pre-lock challenge awarded: %v", res)
	}

	if got := pick("1001", "yuri", 200)["contestant"]; got != "Yuri Gagarin" { // changed pick, fuzzy-free alias
		t.Fatalf("pick = %v", got)
	}
	pick("1002", "Xena Warrior", 200)
	pick("1003", "Nobody At All", 400)
	pick("9999", "Zed", 403)                                                                                                   // not a player here
	call("PUT", "/discord/guilds/7001/channels/7999/champion-pick", `{"discord_user_id":"1001","contestant":"Xena"}`, "", 404) // unbound channel
	call("PUT", "/discord/guilds/7001/channels/7003/champion-pick", `{"discord_user_id":"1001","contestant":"Xena"}`, "", 409) // other season: no round
	call("GET", path+"/champion-round", "", "admin", 200)
	if picks := call("GET", path+"/champion-round", "", "admin", 200)["picks"]; picks != nil {
		t.Fatalf("picks visible before lock: %v", picks)
	}

	// Zed is voted out in Episode 7: can't be picked.
	imp(7, "", champ("survivor-ep7-c1-immunity", "immunity", now.Add(time.Hour), id(x.ID.Bytes)), 200) // replay: unchanged
	imp(8, fmt.Sprintf(`{"position":3,"contestant_id":%q}`, id(z.ID.Bytes)), "", 201)
	pick("1003", "Zed", 400)

	now = locks
	pick("1001", "Xena", 409) // locked: fixed for the season
	call("PUT", path+"/champion-round", round, "admin", 409)
	if picks, ok := call("GET", path+"/champion-round", "", "admin", 200)["picks"].([]any); !ok || len(picks) != 2 {
		t.Fatalf("picks after lock = %v", picks)
	}

	// After the lock: Xena wins immunity (Bob +3), Yuri and Xena share a reward (+2 each).
	ep9 := locks.Add(24 * time.Hour)
	body := champ("survivor-ep9-c1-immunity", "immunity", ep9, id(x.ID.Bytes)) + "," + champ("survivor-ep9-c2-reward", "reward", ep9, id(y.ID.Bytes), id(x.ID.Bytes))
	if res := imp(9, fmt.Sprintf(`{"position":2,"contestant_id":%q}`, id(x.ID.Bytes)), body, 201); res["challenge_awards"] != 3.0 {
		t.Fatalf("ep9 awards = %v", res)
	}
	if got := bonus(); got["Alice"] != 2 || got["Bob"] != 5 || got["Cat"] != 0 {
		t.Fatalf("bonus after ep9 = %v", got)
	}
	imp(9, fmt.Sprintf(`{"position":2,"contestant_id":%q}`, id(x.ID.Bytes)), body, 200) // replay: no double award
	imp(9, fmt.Sprintf(`{"position":2,"contestant_id":%q}`, id(x.ID.Bytes)), champ("survivor-ep9-c1-immunity", "immunity", ep9, id(y.ID.Bytes)), 409)
	// Xena was voted out in Episode 9; she can't win again, so Bob's Champion stops scoring.
	imp(10, "", champ("survivor-ep10-c1-immunity", "immunity", ep9.Add(7*24*time.Hour), id(y.ID.Bytes)), 201)
	if got := bonus(); got["Alice"] != 5 || got["Bob"] != 5 {
		t.Fatalf("bonus after ep10 = %v", got)
	}
}

// The website: pick from a select of castaways still in the game, change it, then see everyone's picks after
// the lock; players outside the season can't pick.
func TestChampionPage(t *testing.T) {
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	instance := createInstanceForTest(t, ctx, q, "Champion page", 51)
	instanceID := uuid.UUID(instance.ID.Bytes).String()
	now := time.Date(2030, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, user := range []string{"amy", "ben"} {
		p := createParticipantForTest(t, ctx, q, instance.ID, user)
		if _, err := pool.Exec(ctx, `UPDATE participants SET discord_user_id = $2 WHERE public_id = $1`, p.ID, user); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []string{"amy", "ben", "outsider"} {
		hash := sha256.Sum256([]byte(user + "-champ"))
		if _, err := pool.Exec(ctx, `INSERT INTO web_sessions (token_hash, discord_user_id, discord_username, kind, expires_at) VALUES ($1,$2,$2,'browser',$3)`, hash[:], user, now.AddDate(0, 1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	createContestantForTest(t, ctx, q, instance.ID, "Xena Warrior")
	createContestantForTest(t, ctx, q, instance.ID, "Yuri Gagarin")
	if _, err := pool.Exec(ctx, `INSERT INTO champion_rounds (instance_id, merge_episode, opens_at, locks_at) SELECT id, 7, $2, $3 FROM instances WHERE public_id = $1`,
		instance.ID, now.Add(-time.Hour), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	const base = "https://castaway.example"
	router := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now }), httpapi.WithPublic(httpapi.PublicConfig{BaseURL: base, InstanceID: instanceID})).PublicRouter()
	serve := func(method, path, form, user string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", base)
		req.AddCookie(&http.Cookie{Name: "castaway_session", Value: user + "-champ"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, rec.Code, status, rec.Body)
		}
		return rec.Body.String()
	}
	if page := serve("GET", "/games", "", "amy", 200); !strings.Contains(page, "Pick Your Champion") {
		t.Fatal("games hub doesn't list Pick Your Champion")
	}
	if page := serve("GET", "/games/champion", "", "amy", 200); !strings.Contains(page, "<option>Xena Warrior</option>") {
		t.Fatalf("no castaway choices: %s", page)
	}
	serve("POST", "/games/champion", "contestant=Xena+Warrior", "amy", 200)
	if page := serve("POST", "/games/champion", "contestant=Yuri+Gagarin", "amy", 200); !strings.Contains(page, "Your Champion is Yuri Gagarin.") {
		t.Fatalf("changed pick: %s", page)
	}
	serve("POST", "/games/champion", "contestant=Xena+Warrior", "ben", 200)
	serve("POST", "/games/champion", "contestant=Xena+Warrior", "outsider", 403)
	if page := serve("GET", "/games/champion", "", "amy", 200); strings.Contains(page, "ben") {
		t.Fatal("picks visible before the lock")
	}
	now = now.Add(25 * time.Hour)
	page := serve("GET", "/games/champion", "", "amy", 200)
	if !strings.Contains(page, "<td>ben</td><td>Xena Warrior</td>") || !strings.Contains(page, "Your Champion: <b>Yuri Gagarin</b>") || strings.Contains(page, `name="contestant"`) {
		t.Fatalf("locked page: %s", page)
	}
	serve("POST", "/games/champion", "contestant=Xena+Warrior", "amy", 409)
}
