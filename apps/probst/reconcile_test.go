package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeSeason is a tiny stand-in for the API the reconciler uses.
type fakeSeason struct {
	t        *testing.T
	imports  map[int][]int
	games    []liveGame
	alerts   map[string]string
	actions  map[string]map[string]any
	posts    map[string]map[string]any
	snapshot []scoreRow
	imported []map[string]any
	// fingerprint is the standings' fingerprint the snapshot reports; empty means "fp".
	fingerprint string
}

func (s *fakeSeason) call(_ context.Context, method, path string, body, out any) error {
	set := func(v any) {
		raw, _ := json.Marshal(v)
		if err := json.Unmarshal(raw, out); err != nil {
			s.t.Fatal(err)
		}
	}
	switch {
	case method == "GET" && strings.HasSuffix(path, "/scheduled-games"):
		set(map[string]any{"games": s.games})
	case method == "GET" && strings.HasSuffix(path, "/episode-imports"):
		var list []map[string]any
		for ep, pos := range s.imports {
			list = append(list, map[string]any{"episode_number": ep, "boot_positions": pos})
		}
		set(map[string]any{"imports": list})
	case method == "POST" && strings.HasSuffix(path, "/episode-imports"):
		b := body.(map[string]any)
		s.imported = append(s.imported, b)
		var pos []int
		for _, x := range b["boots"].([]map[string]any) {
			pos = append(pos, x["position"].(int))
		}
		s.imports[b["episode_number"].(int)] = pos
		set(map[string]any{"status": "applied"})
	case method == "GET" && path == "/instances/x":
		set(map[string]any{"episodes": []map[string]any{{"episode_number": 3, "airs_at": "2026-10-08T00:00:00Z"}}})
	case method == "PUT" && strings.Contains(path, "/admin-alerts/"):
		key := path[strings.LastIndex(path, "/")+1:]
		_, seen := s.alerts[key]
		b, ok := body.(map[string]any)
		if !ok {
			b = map[string]any{"body": body.(map[string]string)["body"]}
		}
		s.alerts[key] = b["body"].(string)
		if b["action"] != nil {
			s.actions[key] = b["action"].(map[string]any)
		}
		set(map[string]any{"created": !seen})
	case method == "PUT" && strings.Contains(path, "/score-snapshots/"):
		fp := cmp.Or(s.fingerprint, "fp")
		set(map[string]any{"fingerprint": fp, "leaderboard": s.snapshot})
	case method == "GET" && strings.Contains(path, "/score-snapshots/"):
		return fmt.Errorf("HTTP 404: no snapshot")
	case method == "GET" && strings.Contains(path, "/leaderboard"):
		set(map[string]any{"leaderboard": []scoreRow{{ID: "a", Total: 3}, {ID: "b", Total: 1}}})
	case method == "GET" && strings.HasSuffix(path, "/outcomes"):
		set(map[string]any{"outcomes": []map[string]any{{"position": 19, "contestant_name": "Kyle Ostwald"}}})
	case method == "PUT" && strings.Contains(path, "/score-posts/"):
		s.posts[path[strings.LastIndex(path, "/")+1:]] = body.(map[string]any)
		set(map[string]any{"action": "created"})
	case method == "GET" && strings.Contains(path, "/game-results/"):
		set(map[string]any{"type": "press_the_button", "tribes": []map[string]any{{"name": "Savu", "emoji": "🟣", "value": 30.0}},
			"players": []map[string]any{{"name": "Ann", "tribe_emoji": "🟣", "presses": 20.0, "points": 3.0, "first": true}, {"name": "Bo", "presses": 10.0, "points": -1.0, "last": true}}})
	case method == "GET" && strings.HasSuffix(path, "/announcements"):
		set(map[string]any{"announcements": []any{}})
	default:
		s.t.Fatalf("unexpected %s %s", method, path)
	}
	return nil
}

func TestReconcile(t *testing.T) {
	f, err := loadSeasonFile("testdata/season-brainland.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, airs, err := expandSeason(f, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeSeason{t: t, imports: map[int][]int{}, alerts: map[string]string{}, actions: map[string]map[string]any{}, posts: map[string]map[string]any{},
		games:    []liveGame{{Type: "press_the_button", Episode: 3}},
		snapshot: []scoreRow{{ID: "a", Name: "Ann", DiscordID: "1", Total: 6, HasDraft: true}, {ID: "b", Name: "Bo", DiscordID: "2", Total: 2, HasDraft: true}}}
	plan := episodePlan{Episode: 3, Status: "incomplete", Why: "challenge 4 has no results yet"}
	var out bytes.Buffer
	r := &reconciler{ctx: context.Background(), call: fake.call, f: f, airs: airs, tmpl: mustTemplates(t, "testdata/templates"), out: &out,
		path: "/instances/x", fetchPlan: func(ep int) (episodePlan, error) {
			if ep != 3 {
				return episodePlan{Episode: ep, Status: "absent"}, nil
			}
			return plan, nil
		}}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, eastern())
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	run := func(now string) {
		t.Helper()
		r.now = at(now)
		if err := r.run(); err != nil {
			t.Fatalf("%s: %v\n%s", now, err, out.String())
		}
	}

	// Ep 3 aired Oct 7; survivoR is behind. Before the due time: wait quietly; no post yet.
	run("2026-10-10 12:00")
	if !strings.Contains(fake.alerts["s51-nudge-2026-10-10"], "Next week's game isn't ready") {
		t.Fatalf("Oct 10 nudge: %v", fake.alerts)
	}
	if len(fake.imported) != 0 || fake.alerts["s51-ep3-results-missing"] != "" {
		t.Fatalf("early: %v %v", fake.imported, fake.alerts)
	}
	// Past due (Tue 11:59pm): one alert, deduped.
	run("2026-10-14 08:00")
	run("2026-10-14 08:05")
	if !strings.Contains(fake.alerts["s51-ep3-results-missing"], "Episode 3 results aren't in survivoR") {
		t.Fatalf("missing alert: %v", fake.alerts)
	}
	// Holds: alert, don't import.
	plan = episodePlan{Episode: 3, Status: "ready", Holds: []string{"Kyle left by \"Quit\""}}
	run("2026-10-14 09:00")
	if len(fake.imported) != 0 || len(fake.alerts) != 4 { // + next-game alert + Oct 10 nudge
		t.Fatalf("holds: %v %v", fake.imported, fake.alerts)
	}
	// 10am nudge: one DM coalescing everything still open (results, holds, next game); once a day.
	run("2026-10-14 10:00")
	run("2026-10-14 10:05")
	nudge := fake.alerts["s51-nudge-2026-10-14"]
	for _, want := range []string{"Daily reminder", "needs a decision", "Next week's game isn't ready"} {
		if !strings.Contains(nudge, want) {
			t.Fatalf("nudge missing %q: %q", want, nudge)
		}
	}
	if strings.Contains(nudge, "aren't in survivoR") { // resolved issues drop out
		t.Fatalf("stale issue in nudge: %q", nudge)
	}
	// Two people left: DM the exact import for a "yes" instead of importing it.
	plan = episodePlan{Episode: 3, Status: "ready", Review: []string{"2 people left this episode"},
		Boots: []planBoot{{Name: "Kyle Ostwald", ContestantID: "k", Position: 19, Result: "Voted out"}, {Name: "Bo Bee", ContestantID: "b", Position: 18, Result: "Quit"}}}
	run("2026-10-14 11:00")
	action := fake.actions["s51-ep3-import"]
	if len(fake.imported) != 0 || action["kind"] != "episode_import" || len(action["payload"].(map[string]any)["boots"].([]map[string]any)) != 2 ||
		!strings.Contains(fake.alerts["s51-ep3-import"], "Bo Bee: place 18 (Quit)") {
		t.Fatalf("proposal: %v %v %v", fake.imported, action, fake.alerts["s51-ep3-import"])
	}
	// 8:05pm Wednesday: the game isn't scored yet; the post waits (alert only after 8:15).
	plan = episodePlan{Episode: 3, Status: "ready", Boots: []planBoot{{Name: "Kyle Ostwald", ContestantID: "k", Position: 19}},
		Challenges: []planChallenge{{Key: "survivor-ep3-c5-immunity", Kind: "immunity", Tribes: []string{"Toka"}}}}
	run("2026-10-14 20:05")
	if len(fake.imported) != 1 || len(fake.posts) != 0 {
		t.Fatalf("import / no post yet: %v %v", fake.imported, fake.posts)
	}
	// The challenge time is the server's episode time + 1h, as `probst challenge` uses.
	if got := fake.imported[0]["challenges"].([]map[string]any)[0]["effective_at"]; got != "2026-10-08T01:00:00Z" {
		t.Fatalf("effective_at %v", got)
	}
	run("2026-10-14 20:20")
	if !strings.Contains(fake.alerts["s51-week-3-scores-waiting"], "Press the Button scoring") {
		t.Fatalf("waiting alert: %v", fake.alerts)
	}
	// A configured game that was never created also blocks the draft.
	saved := fake.games
	fake.games = nil
	run("2026-10-14 20:22")
	if len(fake.posts) != 0 {
		t.Fatal("drafted without the week's game")
	}
	fake.games = saved
	// Scored: drafted for approval with the 8pm time, pings on, the boot spoiler-tagged.
	fake.games[0].Scored = true
	run("2026-10-14 20:25")
	post := fake.posts["s51-week-3-scores"]
	body, _ := post["body"].(string)
	if post == nil || post["send_at"] != at("2026-10-14 20:00").UTC().Format(time.RFC3339) || post["score_fingerprint"] != "fp" || post["notify_users"] != true ||
		post["guild_id"] != "1078197143501819915" || !strings.Contains(body, "||  Kyle") || !strings.Contains(body, "Week 3") || !strings.Contains(body, "<@1>") {
		t.Fatalf("post: %v", post)
	}
	// The week's game results come from games/press_the_button.md.
	if !strings.Contains(body, "🟣 Ann: 20 presses (+3)") || !strings.Contains(body, "Last to press: Bo.") || !strings.Contains(body, "🟣 Savu 30 presses") {
		t.Fatalf("game results missing:\n%s", body)
	}
	t.Log("\n" + body)
	if len(fake.imported) != 1 {
		t.Fatal("re-imported")
	}
	// Past the 3-hour window: no redraft, an expiry alert.
	delete(fake.posts, "s51-week-3-scores")
	run("2026-10-14 23:30")
	if len(fake.posts) != 0 || fake.alerts["s51-week-3-scores-expired"] == "" {
		t.Fatalf("expired: %v %v", fake.posts, fake.alerts)
	}
	// Kill switch.
	r.f.Automation.Enabled = false
	before := len(fake.alerts)
	run("2026-10-21 21:00")
	if len(fake.alerts) != before || !strings.Contains(out.String(), "automation is off") {
		t.Fatal("ran while disabled")
	}
}

func mustTemplates(t *testing.T, dir string) templateSet {
	t.Helper()
	set, err := loadTemplates(dir)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// The game-day reminder is drafted for approval once the game is open and approval_lead (12h) before it sends
// at weekly.reminder.at (never pinging), uses reminder.md, and isn't drafted for a game that closes before it
// or after it has expired.
func TestReconcileReminder(t *testing.T) {
	f, err := loadSeasonFile("testdata/season-brainland.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.Weekly.Reminder.At = "+7d 10:00"
	_, airs, err := expandSeason(f, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, eastern())
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	game := liveGame{Type: "press_the_button", Episode: 3, OpensAt: at("2026-10-07 20:00"), CutoffAt: at("2026-10-14 19:59")}
	fake := &fakeSeason{t: t, imports: map[int][]int{}, alerts: map[string]string{}, actions: map[string]map[string]any{}, posts: map[string]map[string]any{},
		games: []liveGame{game}}
	r := &reconciler{ctx: context.Background(), call: fake.call, f: f, airs: airs, tmpl: mustTemplates(t, "testdata/templates"), out: io.Discard, path: "/instances/x"}
	draft := func(now string) map[string]any {
		t.Helper()
		r.now = at(now)
		delete(fake.posts, "s51-week-3-reminder")
		if err := r.draftReminders(); err != nil {
			t.Fatal(err)
		}
		return fake.posts["s51-week-3-reminder"]
	}
	if draft("2026-10-07 19:00") != nil {
		t.Fatal("drafted before the game opened")
	}
	if draft("2026-10-13 21:59") != nil {
		t.Fatal("drafted more than approval_lead before the reminder")
	}
	post := draft("2026-10-13 22:00")
	body, _ := post["body"].(string)
	if post == nil || post["send_at"] != at("2026-10-14 10:00").UTC().Format(time.RFC3339) || post["notify_users"] != false || post["score_fingerprint"] != nil ||
		!strings.Contains(body, "**Press the Button**") || !strings.Contains(body, "until 7:59pm tonight") {
		t.Fatalf("reminder: %v", post)
	}
	if draft("2026-10-14 13:30") != nil {
		t.Fatal("drafted after it expired")
	}
	fake.games[0].CutoffAt = at("2026-10-14 09:00")
	if draft("2026-10-13 23:00") != nil {
		t.Fatal("reminder after the game closes")
	}
}

// A scores post is drafted approval_lead (12h) before its time once results are in, with a placeholder while
// its game isn't scored; the placeholder draft is never sent, and at the time an unscored game blocks it.
func TestReconcileEarlyDraft(t *testing.T) {
	f, err := loadSeasonFile("testdata/season-brainland.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, airs, err := expandSeason(f, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, eastern())
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	fake := &fakeSeason{t: t, imports: map[int][]int{}, alerts: map[string]string{}, actions: map[string]map[string]any{}, posts: map[string]map[string]any{},
		games:    []liveGame{{Type: "press_the_button", Episode: 3}},
		snapshot: []scoreRow{{ID: "a", Name: "Ann", DiscordID: "1", Total: 6, HasDraft: true}, {ID: "b", Name: "Bo", DiscordID: "2", Total: 2, HasDraft: true}}}
	var out bytes.Buffer
	r := &reconciler{ctx: context.Background(), call: fake.call, f: f, airs: airs, tmpl: mustTemplates(t, "testdata/templates"), out: &out, path: "/instances/x"}
	done := map[int][]int{3: {19}} // episode 3's results are in
	draft := func(now string) {
		t.Helper()
		r.now = at(now)
		if err := r.draftScoresPosts(done); err != nil {
			t.Fatalf("%s: %v\n%s", now, err, out.String())
		}
	}
	sendAt := at("2026-10-14 20:00").UTC().Format(time.RFC3339)

	// 12 hours before the 8pm post: drafted with a placeholder, held for approval (awaiting_results).
	draft("2026-10-14 07:59")
	if len(fake.posts) != 0 {
		t.Fatalf("drafted before approval_lead: %v", fake.posts)
	}
	draft("2026-10-14 08:00")
	post := fake.posts["s51-week-3-scores"]
	body, _ := post["body"].(string)
	if post == nil || post["send_at"] != sendAt || post["awaiting_results"] != true || !strings.Contains(body, "[Press the Button results go here once it's scored]") {
		t.Fatalf("early draft: %v", post)
	}
	// Still unscored at the post's time: no redraft, the waiting alert instead (the server holds the draft).
	delete(fake.posts, "s51-week-3-scores")
	draft("2026-10-14 20:20")
	if len(fake.posts) != 0 || !strings.Contains(fake.alerts["s51-week-3-scores-waiting"], "Press the Button scoring") {
		t.Fatalf("unscored at send time: posts=%v alerts=%v", fake.posts, fake.alerts)
	}
	// Scored before the post's time: redrafted with the results, no placeholder, and the fingerprint moved.
	fake.games[0].Scored = true
	fake.fingerprint = "fp2"
	draft("2026-10-14 19:59")
	post = fake.posts["s51-week-3-scores"]
	body, _ = post["body"].(string)
	if post == nil || post["awaiting_results"] != false || post["score_fingerprint"] != "fp2" || strings.Contains(body, "results go here") ||
		!strings.Contains(body, "🟣 Ann: 20 presses (+3)") {
		t.Fatalf("redraft after scoring: %v", post)
	}
	// The server fills the results into an admin-edited draft: it gets the section and the placeholder line it replaces.
	if gr, _ := post["game_results"].(string); !strings.Contains(gr, "🟣 Ann: 20 presses (+3)") || post["results_placeholder"] != "[Press the Button results go here once it's scored]" {
		t.Fatalf("redraft without results for an edited draft: %v", post)
	}
}

// approval_lead sets how early the scores post is drafted; it defaults to 12 hours.
func TestApprovalLead(t *testing.T) {
	var f seasonFile
	if d, err := approvalLead(f); err != nil || d != 12*time.Hour {
		t.Fatalf("default: %v %v", d, err)
	}
	f.Automation.ApprovalLead = "6h30m"
	if d, err := approvalLead(f); err != nil || d != 6*time.Hour+30*time.Minute {
		t.Fatalf("6h30m: %v %v", d, err)
	}
	for _, bad := range []string{"soon", "-1h", "0"} {
		f.Automation.ApprovalLead = bad
		if _, err := approvalLead(f); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
