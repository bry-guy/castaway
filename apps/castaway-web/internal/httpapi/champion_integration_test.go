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
// winners' pickers (+3 immunity, +1 reward); a voted-out Champion can't be picked and stops scoring.
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
	opens, locks := now, now.Add(14*24*time.Hour)
	round := fmt.Sprintf(`{"merge_episode":7,"opens_at":%q,"locks_at":%q}`, opens.Format(time.RFC3339), locks.Format(time.RFC3339))
	call("PUT", path+"/champion-round", round, "nobody", 403)
	call("PUT", path+"/champion-round", round, "admin", 200)

	wikiImp := func(ep int, replace bool, boots, champs string, want int) map[string]any {
		return call("POST", path+"/episode-imports", fmt.Sprintf(`{"episode_number":%d,"source":"wiki","replace":%t,"source_revision":"w","boots":[%s],"challenges":[],"champion_challenges":[%s]}`, ep, replace, boots, champs), "admin", want)
	}

	// A pick counts for a challenge if held 45 minutes before it (challenges sit at air time + 1h, so 8:15pm).
	ep7 := now.Add(time.Hour)
	pick("1001", "Xena", 200)
	pick("1003", "Zed", 200)
	if res := imp(7, "", champ("survivor-ep7-c1-immunity", "immunity", ep7, id(x.ID.Bytes)), 201); res["challenge_awards"] != 1.0 {
		t.Fatalf("ep7 awards = %v", res)
	}
	pick("1001", "yuri", 409) // a pick is for the rest of the season
	now = now.Add(30 * time.Minute)
	pick("1002", "Xena Warrior", 200)                                                   // after 8:15: counts from the next episode
	imp(7, "", champ("survivor-ep7-c1-immunity", "immunity", ep7, id(x.ID.Bytes)), 200) // replay: unchanged
	if got := bonus(); got["Alice"] != 3 || got["Bob"] != 0 || got["Cat"] != 0 {
		t.Fatalf("bonus after ep7 = %v", got)
	}
	pick("1003", "Nobody At All", 400)
	pick("9999", "Zed", 403)                                                                                                   // not a player here
	call("PUT", "/discord/guilds/7001/channels/7999/champion-pick", `{"discord_user_id":"1001","contestant":"Xena"}`, "", 404) // unbound channel
	call("PUT", "/discord/guilds/7001/channels/7003/champion-pick", `{"discord_user_id":"1001","contestant":"Xena"}`, "", 409) // other season: no round
	if picks := call("GET", path+"/champion-round", "", "admin", 200)["picks"]; picks != nil {
		t.Fatalf("picks visible before lock: %v", picks)
	}

	// Episode 8 (from the wiki): Zed wins a reward, then is voted out. Cat's reward stands after she picks again,
	// including when the episode is re-imported.
	ep8 := ep7.Add(7 * 24 * time.Hour)
	zedOut := fmt.Sprintf(`{"position":3,"contestant_id":%q}`, id(z.ID.Bytes))
	if res := wikiImp(8, false, zedOut, champ("wiki-ep8-c1-reward", "reward", ep8, id(z.ID.Bytes)), 201); res["challenge_awards"] != 1.0 {
		t.Fatalf("ep8 awards = %v", res)
	}
	now = ep8.Add(time.Hour)
	pick("1003", "Zed", 400) // out of the game
	pick("1003", "Yuri", 200)
	wikiImp(8, true, zedOut, champ("wiki-ep8-c1-reward", "reward", ep8, id(z.ID.Bytes))+","+champ("wiki-ep8-c2-immunity", "immunity", ep8, id(x.ID.Bytes)), 200)
	if got := bonus(); got["Alice"] != 6 || got["Bob"] != 3 || got["Cat"] != 1 {
		t.Fatalf("bonus after re-imported ep8 = %v", got)
	}

	now = locks
	pick("1003", "Xena", 409) // locked
	call("PUT", path+"/champion-round", round, "admin", 409)
	if picks, ok := call("GET", path+"/champion-round", "", "admin", 200)["picks"].([]any); !ok || len(picks) != 3 {
		t.Fatalf("picks after lock = %v", picks)
	}

	// Xena wins immunity (Alice, Bob +3), Yuri and Xena share a reward (+1 each); Xena is voted out.
	ep9 := locks.Add(24 * time.Hour)
	body := champ("survivor-ep9-c1-immunity", "immunity", ep9, id(x.ID.Bytes)) + "," + champ("survivor-ep9-c2-reward", "reward", ep9, id(y.ID.Bytes), id(x.ID.Bytes))
	if res := imp(9, fmt.Sprintf(`{"position":2,"contestant_id":%q}`, id(x.ID.Bytes)), body, 201); res["challenge_awards"] != 5.0 {
		t.Fatalf("ep9 awards = %v", res)
	}
	imp(9, fmt.Sprintf(`{"position":2,"contestant_id":%q}`, id(x.ID.Bytes)), body, 200) // replay: no double award
	imp(9, fmt.Sprintf(`{"position":2,"contestant_id":%q}`, id(x.ID.Bytes)), champ("survivor-ep9-c1-immunity", "immunity", ep9, id(y.ID.Bytes)), 409)
	// Xena is out, so she can't win again; Yuri can.
	imp(10, "", champ("survivor-ep10-c1-immunity", "immunity", ep9.Add(7*24*time.Hour), id(y.ID.Bytes)), 201)
	if got := bonus(); got["Alice"] != 10 || got["Bob"] != 7 || got["Cat"] != 5 {
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
	yuri := createContestantForTest(t, ctx, q, instance.ID, "Yuri Gagarin")
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
	// Listed under the latest episode aired when it opens (8), not the merge episode (7).
	for ep, airs := range map[int]time.Time{7: now.AddDate(0, 0, -14), 8: now.AddDate(0, 0, -7), 9: now.Add(time.Hour)} {
		if _, err := pool.Exec(ctx, `INSERT INTO instance_episodes (instance_id, episode_number, label, airs_at) SELECT id, $2, 'ep', $3 FROM instances WHERE public_id = $1`, instance.ID, ep, airs); err != nil {
			t.Fatal(err)
		}
	}
	if page := serve("GET", "/games", "", "amy", 200); !strings.Contains(page, "<h3>Episode 8</h3><div class=\"game-list\"><a class=\"game-card\" href=\"/games/champion\">") {
		t.Fatalf("games hub doesn't list Pick Your Champion under Episode 8: %s", page)
	}
	if page := serve("GET", "/games/champion", "", "amy", 200); !strings.Contains(page, "<option>Xena Warrior</option>") {
		t.Fatalf("no castaway choices: %s", page)
	}
	if page := serve("POST", "/games/champion", "contestant=Yuri+Gagarin", "amy", 200); !strings.Contains(page, "Your Champion is Yuri Gagarin.") {
		t.Fatalf("pick: %s", page)
	}
	if page := serve("POST", "/games/champion", "contestant=Xena+Warrior", "amy", 409); !strings.Contains(page, "Yuri Gagarin is your Champion for the rest of the season") {
		t.Fatalf("changed pick: %s", page)
	}
	if page := serve("GET", "/games/champion", "", "amy", 200); !strings.Contains(page, "Your Champion: <b>Yuri Gagarin</b>, for the rest of the season.") || strings.Contains(page, `name="contestant"`) {
		t.Fatalf("held pick page: %s", page)
	}
	serve("POST", "/games/champion", "contestant=Xena+Warrior", "ben", 200)
	serve("POST", "/games/champion", "contestant=Xena+Warrior", "outsider", 403)
	if page := serve("GET", "/games/champion", "", "amy", 200); strings.Contains(page, "ben") {
		t.Fatal("picks visible before the lock")
	}
	// Yuri is voted out before the lock: amy is told to pick again, and nothing is preselected.
	if _, err := pool.Exec(ctx, `INSERT INTO outcome_positions (instance_id, position, contestant_id)
		SELECT i.id, 18, c.id FROM instances i, contestants c WHERE i.public_id = $1 AND c.public_id = $2`, instance.ID, yuri.ID); err != nil {
		t.Fatal(err)
	}
	if page := serve("GET", "/games/champion", "", "amy", 200); !strings.Contains(page, "Yuri Gagarin is out of the game: pick again.") || !strings.Contains(page, `disabled selected>Choose a castaway`) || strings.Contains(page, "<option>Yuri") {
		t.Fatalf("booted pick: %s", page)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outcome_positions op USING instances i WHERE op.instance_id = i.id AND i.public_id = $1`, instance.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	page := serve("GET", "/games/champion", "", "amy", 200)
	if !strings.Contains(page, "<td>ben</td><td>Xena Warrior</td>") || !strings.Contains(page, "Your Champion: <b>Yuri Gagarin</b>") || strings.Contains(page, `name="contestant"`) {
		t.Fatalf("locked page: %s", page)
	}
	serve("POST", "/games/champion", "contestant=Xena+Warrior", "amy", 409)
}
