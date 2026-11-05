package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// TestSeasonAutomation covers the all-or-nothing episode import (replay, conflicts, legacy keys, rollback),
// weekly standings snapshots, and automated scores posts that never send stale numbers.
func TestSeasonAutomation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	instance := createInstanceForTest(t, ctx, q, "Automation", 957)
	other := createInstanceForTest(t, ctx, q, "Other", 958)
	id := func(u [16]byte) string { return uuid.UUID(u).String() }
	a := createParticipantForTest(t, ctx, q, instance.ID, "Alice")
	b := createParticipantForTest(t, ctx, q, instance.ID, "Bob")
	x := createContestantForTest(t, ctx, q, instance.ID, "Xena")
	y := createContestantForTest(t, ctx, q, instance.ID, "Yuri")
	z := createContestantForTest(t, ctx, q, instance.ID, "Zed")
	createDraftPickForTest(t, ctx, q, instance.ID, a.ID, x.ID, 1)
	createDraftPickForTest(t, ctx, q, instance.ID, b.ID, y.ID, 1)
	for _, inst := range []db.CreateInstanceRow{instance, other} {
		if _, err := q.CreateInstanceAdmin(ctx, db.CreateInstanceAdminParams{InstanceID: inst.ID, DiscordUserID: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.SetDiscordChannelBinding(ctx, db.SetDiscordChannelBindingParams{GuildID: "8001", ChannelID: "8002", InstanceID: instance.ID}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 10, 8, 23, 0, 0, 0, time.UTC)
	router := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now }), httpapi.WithServiceAuth(httpapi.ServiceAuthConfig{Enabled: true, BearerTokens: []string{"token"}})).Router()
	path := "/instances/" + id(instance.ID.Bytes)
	call := func(method, url, body string, want int) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, authorizedJSONRequest(method, url, body, "token", "admin"))
		if rec.Code != want {
			t.Fatalf("%s %s: %d %s", method, url, rec.Code, rec.Body.String())
		}
		out := map[string]any{}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	bonus := func() map[string]float64 {
		var res struct {
			Leaderboard []struct {
				Name  string  `json:"participant_name"`
				Bonus float64 `json:"bonus_points"`
			} `json:"leaderboard"`
		}
		raw, err := json.Marshal(call("GET", path+"/leaderboard", "", 200))
		if err != nil || json.Unmarshal(raw, &res) != nil {
			t.Fatal("leaderboard decode", err)
		}
		got := map[string]float64{}
		for _, r := range res.Leaderboard {
			got[r.Name] = r.Bonus
		}
		return got
	}
	tribeStart := time.Date(2030, 10, 1, 0, 0, 0, 0, time.UTC)
	call("PUT", path+"/tribes", fmt.Sprintf(`{"effective_at":%q,"tribes":[{"name":"Savu","color":"purple","participant_ids":[%q]},{"name":"Toka","color":"yellow","participant_ids":[%q]}]}`,
		tribeStart.Format(time.RFC3339), id(a.ID.Bytes), id(b.ID.Bytes)), 200)

	ep2 := "2030-10-03T01:00:00Z"
	// A manual award under the old naming scheme, e.g. `probst challenge immunity Toka --episode 2`.
	call("POST", path+"/tribe-challenges", `{"key":"ep2-immunity","kind":"immunity","winning_tribes":["Toka"],"effective_at":"`+ep2+`"}`, 201)
	body := func(boot string, rewardTribe string) string {
		return fmt.Sprintf(`{"episode_number":2,"source":"survivor","source_revision":"rev1","boots":[{"position":3,"contestant_id":%q}],
			"challenges":[{"key":"survivor-ep2-c3-immunity","legacy_keys":["ep2-immunity"],"kind":"immunity","winning_tribes":["Toka"],"effective_at":%q},
				{"key":"survivor-ep2-c2-reward","legacy_keys":["ep2-reward"],"kind":"reward","winning_tribes":[%q],"effective_at":%q}]}`, boot, ep2, rewardTribe, ep2)
	}

	// A bad tribe in the second challenge rolls back the boot and the first challenge: nothing applied.
	call("POST", path+"/episode-imports", body(id(z.ID.Bytes), "Nobody"), 400)
	if outs := call("GET", path+"/outcomes", "", 200); jlen(outs, "outcomes") != 0 {
		t.Fatalf("partial import left outcomes: %v", outs)
	}
	if got := bonus(); got["Bob"] != 2 || got["Alice"] != 0 {
		t.Fatalf("after rollback: %v", got)
	}
	// A real import reuses the manual immunity (no double award) and adds the reward and the boot.
	if res := call("POST", path+"/episode-imports", body(id(z.ID.Bytes), "Savu"), 201); res["outcomes_recorded"] != 1.0 || res["challenge_awards"] != 2.0 {
		t.Fatalf("import: %v", res)
	}
	if got := bonus(); got["Bob"] != 2 || got["Alice"] != 1 {
		t.Fatalf("after import: %v", got)
	}
	// Same results from a newer upstream revision: nothing changes. Different results: conflict.
	call("POST", path+"/episode-imports", strings.Replace(body(id(z.ID.Bytes), "Savu"), "rev1", "rev2", 1), 200)
	call("POST", path+"/episode-imports", body(id(x.ID.Bytes), "Savu"), 409)
	if got := bonus(); got["Bob"] != 2 || got["Alice"] != 1 {
		t.Fatalf("after replays: %v", got)
	}
	// A manual award that disagrees with upstream is a conflict, not an overwrite.
	call("POST", path+"/tribe-challenges", `{"key":"ep3-reward","kind":"reward","winning_tribes":["Toka"],"effective_at":"2030-10-10T01:00:00Z"}`, 201)
	call("POST", path+"/episode-imports", `{"episode_number":3,"source":"survivor","boots":[],"challenges":[{"key":"survivor-ep3-c5-reward","legacy_keys":["ep3-reward"],"kind":"reward","winning_tribes":["Savu"],"effective_at":"2030-10-10T01:00:00Z"}]}`, 409)
	// Two rewards in one episode: the import can't tell which one a hand-entered `--key` reward was, so it
	// refuses; and a hand entry can't double an imported challenge.
	call("POST", path+"/tribe-challenges", `{"key":"ep4-reward-b","kind":"reward","winning_tribes":["Savu"],"effective_at":"2030-10-17T01:00:00Z"}`, 201)
	call("POST", path+"/episode-imports", `{"episode_number":4,"source":"survivor","boots":[],"challenges":[{"key":"survivor-ep4-c7-reward","kind":"reward","winning_tribes":["Savu"],"effective_at":"2030-10-17T01:00:00Z"},{"key":"survivor-ep4-c8-reward","kind":"reward","winning_tribes":["Toka"],"effective_at":"2030-10-17T01:00:00Z"}]}`, 409)
	call("POST", path+"/episode-imports", `{"episode_number":5,"source":"survivor","boots":[],"challenges":[{"key":"survivor-ep5-c9-reward","kind":"reward","winning_tribes":["Savu"],"effective_at":"2030-10-24T01:00:00Z"},{"key":"survivor-ep5-c10-reward","kind":"reward","winning_tribes":["Toka"],"effective_at":"2030-10-24T01:00:00Z"}]}`, 201)
	call("POST", path+"/tribe-challenges", `{"key":"ep5-reward","kind":"reward","winning_tribes":["Savu"],"effective_at":"2030-10-24T01:00:00Z"}`, 409)
	imports := call("GET", path+"/episode-imports", "", 200)
	if jlen(imports, "imports") != 2 || jstr(imports, "imports", 0, "source_revision") != "rev1" {
		t.Fatalf("imports: %v", imports)
	}
	// The wiki scores an episode first; survivoR replaces it, reporting whether anything changed.
	wiki := func(ep int, source, key, tribe string, replace bool) string {
		return fmt.Sprintf(`{"episode_number":%d,"source":%q,"replace":%t,"boots":[],"challenges":[{"key":%q,"kind":"reward","winning_tribes":[%q],"effective_at":"2030-10-0%dT01:00:00Z"}]}`, ep, source, replace, key, tribe, ep-1)
	}
	base := bonus()
	call("POST", path+"/episode-imports", wiki(6, "wiki", "wiki-ep6-c1-reward", "Savu", true), 400) // only survivoR replaces
	call("POST", path+"/episode-imports", wiki(6, "wiki", "wiki-ep6-c1-reward", "Savu", false), 201)
	call("POST", path+"/episode-imports", wiki(6, "survivor", "survivor-ep6-c11-reward", "Savu", false), 409)
	if res := call("POST", path+"/episode-imports", wiki(6, "survivor", "survivor-ep6-c11-reward", "Savu", true), 200); res["status"] != "replaced" || res["changed"] != false {
		t.Fatalf("same-results replace: %v", res)
	}
	if got := bonus(); got["Bob"] != base["Bob"] || got["Alice"] != base["Alice"]+1 {
		t.Fatalf("after same replace: %v", got)
	}
	call("POST", path+"/episode-imports", wiki(7, "wiki", "wiki-ep7-c1-reward", "Savu", false), 201)
	if res := call("POST", path+"/episode-imports", wiki(7, "survivor", "survivor-ep7-c12-reward", "Toka", true), 200); res["status"] != "replaced" || res["changed"] != true {
		t.Fatalf("changed replace: %v", res)
	}
	if got := bonus(); got["Bob"] != base["Bob"]+1 || got["Alice"] != base["Alice"]+1 {
		t.Fatalf("after changed replace: %v base %v", got, base)
	}
	// A survivoR import is never replaced.
	call("POST", path+"/episode-imports", wiki(7, "survivor", "survivor-ep7-c12-reward", "Savu", true), 409)
	// Another season sees none of it.
	if got := call("GET", "/instances/"+id(other.ID.Bytes)+"/episode-imports", "", 200); jlen(got, "imports") != 0 {
		t.Fatal("cross-instance import leak")
	}

	// Snapshots and automated scores posts.
	snap := call("PUT", path+"/score-snapshots/2", `{"request_key":"s957-week-2-scores"}`, 200)
	fp := jstr(snap, "fingerprint")
	post := func(text, fingerprint string, sendAt time.Time, want int) string {
		t.Helper()
		res := call("PUT", path+"/score-posts/s957-week-2-scores", fmt.Sprintf(`{"guild_id":"8001","channel_id":"8002","body":%q,"send_at":%q,"score_fingerprint":%q,"notify_users":true}`,
			text, sendAt.Format(time.RFC3339), fingerprint), want)
		return jstr(res, "action")
	}
	sendAt := now.Add(-30 * time.Minute) // drafted after its 8pm time: allowed within 3 hours
	post("Week 2", fp, now.Add(-4*time.Hour), 409)
	post("Week 2", "stale", sendAt, 409)
	if got := post("Week 2: Bob leads", fp, sendAt, 200); got != "created" {
		t.Fatal(got)
	}
	if got := post("Week 2: Bob leads", fp, sendAt, 200); got != "unchanged" {
		t.Fatal(got)
	}
	approval := call("POST", "/announcements/approvals/claim", `{"guild_ids":["8001"]}`, 200)
	annID, revision := jstr(approval, "announcement", "id"), jstr(approval, "revision")
	call("POST", "/announcements/"+annID+"/approval-delivered", fmt.Sprintf(`{"revision":%q}`, revision), 200)
	call("POST", "/announcements/"+annID+"/approve", fmt.Sprintf(`{"admin_discord_user_id":"admin","revision":%q}`, revision), 200)

	// Scores change after approval: the sender refuses it and holds it again.
	call("POST", path+"/tribe-challenges", `{"key":"ep2-extra","kind":"immunity","winning_tribes":["Savu"],"effective_at":"2030-10-03T02:00:00Z"}`, 201)
	if got := call("POST", "/announcements/claim", `{"guild_ids":["8001"]}`, 200); got["announcement"] != nil {
		t.Fatal("stale approved scores post was sent")
	}
	// Re-snapshot and redraft: the untouched draft is replaced and re-asked.
	fp2 := jstr(call("PUT", path+"/score-snapshots/2", `{"request_key":"s957-week-2-scores"}`, 200), "fingerprint")
	if fp2 == fp {
		t.Fatal("fingerprint did not change with scores")
	}
	if got := post("Week 2: tied!", fp2, sendAt, 200); got != "regenerated" {
		t.Fatal(got)
	}
	approval = call("POST", "/announcements/approvals/claim", `{"guild_ids":["8001"]}`, 200)
	if jstr(approval, "announcement", "body") != "Week 2: tied!" {
		t.Fatalf("re-ask: %v", approval)
	}
	call("POST", "/announcements/"+annID+"/approval-delivered", fmt.Sprintf(`{"revision":%q}`, approval["revision"]), 200)
	// An admin edits the copy by DM; later score changes keep the edit and alert the admins.
	call("POST", "/announcements/"+annID+"/revise", fmt.Sprintf(`{"admin_discord_user_id":"admin","revision":%q,"body":"Admin words, tied!"}`, approval["revision"]), 200)
	call("POST", path+"/tribe-challenges", `{"key":"ep2-extra2","kind":"immunity","winning_tribes":["Toka"],"effective_at":"2030-10-03T02:00:00Z"}`, 201)
	fp3 := jstr(call("PUT", path+"/score-snapshots/2", `{"request_key":"s957-week-2-scores"}`, 200), "fingerprint")
	editedRevision := jstr(call("POST", "/announcements/approvals/claim", `{"guild_ids":["8001"]}`, 200), "revision")
	if got := post("Week 2: regenerated", fp3, sendAt, 200); got != "rehold-edited" {
		t.Fatal(got)
	}
	// "yes" to the DM sent before the rescore no longer approves it.
	call("POST", "/announcements/"+annID+"/approve", fmt.Sprintf(`{"admin_discord_user_id":"admin","revision":%q}`, editedRevision), 409)
	approval = call("POST", "/announcements/approvals/claim", `{"guild_ids":["8001"]}`, 200)
	if jstr(approval, "announcement", "body") != "Admin words, tied!" {
		t.Fatalf("admin edit lost: %v", approval)
	}
	alert := call("POST", "/admin-alerts/claim", `{"guild_ids":["8001"]}`, 200)
	if !strings.Contains(jstr(alert, "alert", "body"), "Scores changed after you edited") {
		t.Fatalf("no rescored alert: %v", alert)
	}
	// Approve and send; afterwards the snapshot is frozen and the post is never touched again.
	call("POST", "/announcements/"+annID+"/approval-delivered", fmt.Sprintf(`{"revision":%q}`, approval["revision"]), 200)
	call("POST", "/announcements/"+annID+"/approve", fmt.Sprintf(`{"admin_discord_user_id":"admin","revision":%q}`, approval["revision"]), 200)
	sent := call("POST", "/announcements/claim", `{"guild_ids":["8001"]}`, 200)
	if sent["announcement"] == nil {
		t.Fatal("current approved post did not send")
	}
	call("POST", "/announcements/"+annID+"/finish", `{"message_id":"123456"}`, 200)
	call("POST", path+"/tribe-challenges", `{"key":"ep2-extra3","kind":"immunity","winning_tribes":["Toka"],"effective_at":"2030-10-03T02:00:00Z"}`, 201)
	frozen := call("PUT", path+"/score-snapshots/2", `{"request_key":"s957-week-2-scores"}`, 200)
	if frozen["fingerprint"] != fp3 || frozen["frozen"] != true {
		t.Fatalf("snapshot moved after send: %v", frozen)
	}
	if got := post("Week 2 again", fp3, sendAt, 409); got != "" {
		t.Fatal("stale fingerprint accepted")
	}
	// A scores post drafted before its game was scored carries a placeholder: it's asked for approval as usual,
	// but never sent with the placeholder; a redraft with the results (same standings) replaces it and re-asks.
	fp4 := jstr(call("PUT", path+"/score-snapshots/4", `{"request_key":"s957-week-4-scores"}`, 200), "fingerprint")
	post4 := func(text string, awaiting bool) string {
		t.Helper()
		res := call("PUT", path+"/score-posts/s957-week-4-scores", fmt.Sprintf(`{"guild_id":"8001","channel_id":"8002","body":%q,"send_at":%q,"score_fingerprint":%q,"notify_users":true,"awaiting_results":%t}`,
			text, sendAt.Format(time.RFC3339), fp4, awaiting), 200)
		return jstr(res, "action")
	}
	if got := post4("Week 4: Press the Button results go here once it's scored", true); got != "created" {
		t.Fatal(got)
	}
	ask := call("POST", "/announcements/approvals/claim", `{"guild_ids":["8001"]}`, 200)
	call("POST", "/announcements/"+jstr(ask, "announcement", "id")+"/approval-delivered", fmt.Sprintf(`{"revision":%q}`, ask["revision"]), 200)
	call("POST", "/announcements/"+jstr(ask, "announcement", "id")+"/approve", fmt.Sprintf(`{"admin_discord_user_id":"admin","revision":%q}`, ask["revision"]), 200)
	if got := call("POST", "/announcements/claim", `{"guild_ids":["8001"]}`, 200); got["announcement"] != nil {
		t.Fatal("approved post with a placeholder was sent")
	}
	if got := post4("Week 4: final results", false); got != "regenerated" {
		t.Fatal(got)
	}
	ask = call("POST", "/announcements/approvals/claim", `{"guild_ids":["8001"]}`, 200)
	if jstr(ask, "announcement", "body") != "Week 4: final results" {
		t.Fatalf("redraft not re-asked: %v", ask)
	}
	call("POST", "/announcements/"+jstr(ask, "announcement", "id")+"/approval-delivered", fmt.Sprintf(`{"revision":%q}`, ask["revision"]), 200)
	call("POST", "/announcements/"+jstr(ask, "announcement", "id")+"/approve", fmt.Sprintf(`{"admin_discord_user_id":"admin","revision":%q}`, ask["revision"]), 200)
	if got := call("POST", "/announcements/claim", `{"guild_ids":["8001"]}`, 200); got["announcement"] == nil {
		t.Fatal("redrafted post with results did not send")
	}

	// An admin edits a draft written before its game was scored. Once the game scores, the placeholder line is replaced
	// with the results, the admin's words stay, and the post is re-asked; without the placeholder line it stays held.
	const placeholder = "[Press the Button results go here once it's scored]"
	scoresPost := func(key, fingerprint, body, results string, awaiting bool, want int) string {
		t.Helper()
		res := call("PUT", path+"/score-posts/"+key, fmt.Sprintf(`{"guild_id":"8001","channel_id":"8002","body":%q,"send_at":%q,"score_fingerprint":%q,"notify_users":true,"awaiting_results":%t,"game_results":%q,"results_placeholder":%q}`,
			body, sendAt.Format(time.RFC3339), fingerprint, awaiting, results, placeholder), want)
		return jstr(res, "action")
	}
	editedBeforeResults := func(week int, body string) (key, annID string) {
		t.Helper()
		key = fmt.Sprintf("s957-week-%d-scores", week)
		fp := jstr(call("PUT", fmt.Sprintf("%s/score-snapshots/%d", path, week), fmt.Sprintf(`{"request_key":%q}`, key), 200), "fingerprint")
		if got := scoresPost(key, fp, "Week 5 intro\n"+placeholder+"\nWeek 5 outro", "", true, 200); got != "created" {
			t.Fatal(got)
		}
		ask := call("POST", "/announcements/approvals/claim", `{"guild_ids":["8001"]}`, 200)
		annID = jstr(ask, "announcement", "id")
		call("POST", "/announcements/"+annID+"/approval-delivered", fmt.Sprintf(`{"revision":%q}`, ask["revision"]), 200)
		call("POST", "/announcements/"+annID+"/revise", fmt.Sprintf(`{"admin_discord_user_id":"admin","revision":%q,"body":%q}`, ask["revision"], body), 200)
		return key, annID
	}
	var count int
	// Edited, placeholder kept: the results go in its place, the admin's text stays, and the post is re-asked.
	key5, ann5 := editedBeforeResults(5, "Admin intro\n"+placeholder+"\nAdmin outro")
	call("POST", path+"/tribe-challenges", `{"key":"ep5-extra","kind":"immunity","winning_tribes":["Toka"],"effective_at":"2030-10-03T02:00:00Z"}`, 201)
	fp5 := jstr(call("PUT", path+"/score-snapshots/5", fmt.Sprintf(`{"request_key":%q}`, key5), 200), "fingerprint")
	if got := scoresPost(key5, fp5, "Week 5 rendered", "Press the Button: Ann 20", false, 200); got != "results-inserted" {
		t.Fatal(got)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_alerts a JOIN instances i ON i.id = a.instance_id
		WHERE i.public_id = $1 AND a.alert_key = $2`, id(instance.ID.Bytes), key5+"-rescored-"+fp5[:12]).Scan(&count); err != nil || count != 1 {
		t.Fatalf("no standings alert for the edited draft: count=%d err=%v", count, err)
	}
	ask = call("POST", "/announcements/approvals/claim", `{"guild_ids":["8001"]}`, 200)
	if jstr(ask, "announcement", "id") != ann5 || jstr(ask, "announcement", "body") != "Admin intro\nPress the Button: Ann 20\nAdmin outro" {
		t.Fatalf("results not inserted into the admin's draft: %v", ask)
	}
	call("POST", "/announcements/"+ann5+"/approval-delivered", fmt.Sprintf(`{"revision":%q}`, ask["revision"]), 200)
	call("POST", "/announcements/"+ann5+"/approve", fmt.Sprintf(`{"admin_discord_user_id":"admin","revision":%q}`, ask["revision"]), 200)
	if sent := call("POST", "/announcements/claim", `{"guild_ids":["8001"]}`, 200); sent["announcement"] == nil || jstr(sent, "announcement", "body") != "Admin intro\nPress the Button: Ann 20\nAdmin outro" {
		t.Fatalf("approved draft with results did not send: %v", sent)
	}
	call("POST", "/announcements/"+ann5+"/finish", `{"message_id":"555"}`, 200)

	// Edited, placeholder removed by the admin: the results can't go in, so the post stays held and the admins are alerted.
	key6, ann6 := editedBeforeResults(6, "Admin intro without the line\nAdmin outro")
	call("POST", path+"/tribe-challenges", `{"key":"ep6-extra","kind":"immunity","winning_tribes":["Savu"],"effective_at":"2030-10-03T02:00:00Z"}`, 201)
	fp6 := jstr(call("PUT", path+"/score-snapshots/6", fmt.Sprintf(`{"request_key":%q}`, key6), 200), "fingerprint")
	if got := scoresPost(key6, fp6, "Week 6 rendered", "Press the Button: Bo 2", false, 200); got != "results-missing" {
		t.Fatal(got)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM admin_alerts a JOIN instances i ON i.id = a.instance_id
		WHERE i.public_id = $1 AND a.alert_key = $2`, id(instance.ID.Bytes), key6+"-results-missing").Scan(&count); err != nil || count != 1 {
		t.Fatalf("no results-missing alert: count=%d err=%v", count, err)
	}
	var awaiting bool
	var stored string
	if err := pool.QueryRow(ctx, `SELECT a.awaiting_results, a.body FROM announcements a WHERE a.id = $1`, ann6).Scan(&awaiting, &stored); err != nil || !awaiting || stored != "Admin intro without the line\nAdmin outro" {
		t.Fatalf("held draft changed: awaiting=%v body=%q err=%v", awaiting, stored, err)
	}
	if got := call("POST", "/announcements/claim", `{"guild_ids":["8001"]}`, 200); got["announcement"] != nil {
		t.Fatal("held draft without results was sent")
	}

	// A plain automated post (weekly game reminder) has no score fingerprint: it's held for approval like a
	// scores post, a redraft with the same words is a no-op, and new words replace an unedited draft.
	remind := func(text string) string {
		t.Helper()
		return jstr(call("PUT", path+"/score-posts/s957-week-3-reminder", fmt.Sprintf(`{"guild_id":"8001","channel_id":"8002","body":%q,"send_at":%q}`,
			text, now.Add(time.Hour).Format(time.RFC3339)), 200), "action")
	}
	if got := remind("Game closes at 7:59pm"); got != "created" {
		t.Fatal(got)
	}
	if got := remind("Game closes at 7:59pm"); got != "unchanged" {
		t.Fatal(got)
	}
	if got := remind("Game closes at 7:59pm tonight"); got != "regenerated" {
		t.Fatal(got)
	}
	var gated bool
	var fingerprint *string
	if err := pool.QueryRow(ctx, `SELECT approval_gated, score_fingerprint FROM announcements WHERE request_key = 's957-week-3-reminder'`).Scan(&gated, &fingerprint); err != nil || !gated || fingerprint != nil {
		t.Fatalf("reminder: gated=%v fingerprint=%v err=%v", gated, fingerprint, err)
	}
}

// jget walks decoded JSON by keys (string) and indexes (int); missing paths give nil.
func jget(v any, path ...any) any {
	for _, k := range path {
		switch k := k.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			l, ok := v.([]any)
			if !ok || k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func jstr(v any, path ...any) string {
	if s, ok := jget(v, path...).(string); ok {
		return s
	}
	return ""
}

func jlen(v any, path ...any) int {
	if l, ok := jget(v, path...).([]any); ok {
		return len(l)
	}
	return 0
}
