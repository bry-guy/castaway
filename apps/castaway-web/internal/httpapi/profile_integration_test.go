package httpapi_test

import (
	"crypto/sha256"
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

// Profiles: league-only history joined by Discord account, secret points hidden, soft-closed draft reveal.
func TestProfiles(t *testing.T) {
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	q := db.New(pool)
	now := time.Date(2026, 10, 2, 16, 0, 0, 0, time.UTC)
	current := createInstanceForTest(t, ctx, q, "Season 51", 51)
	past := createInstanceForTest(t, ctx, q, "Season 50", 50)
	other := createInstanceForTest(t, ctx, q, "Season 51 (BrainLand)", 51)
	type person struct{ cur, past, other pgtype.UUID }
	people := map[string]*person{}
	link := func(inst pgtype.UUID, name, discord string) pgtype.UUID {
		p := createParticipantForTest(t, ctx, q, inst, name)
		if discord != "" {
			if _, err := q.SetParticipantDiscordUserID(ctx, db.SetParticipantDiscordUserIDParams{ID: p.ID, DiscordUserID: pgtype.Text{String: discord, Valid: true}}); err != nil {
				t.Fatal(err)
			}
		}
		return p.ID
	}
	for _, user := range []string{"alice", "bob", "carol"} {
		people[user] = &person{cur: link(current.ID, user, user)}
	}
	people["alice"].past = link(past.ID, "Alice (S50)", "alice")
	people["alice"].other = link(other.ID, "Alice BrainLand", "alice")
	outsider := link(past.ID, "Old Timer", "dave") // past league player, not in the current season

	torch := createContestantForTest(t, ctx, q, current.ID, "Torch Bearer")
	pastPick := createContestantForTest(t, ctx, q, past.ID, "Old Legend")
	createDraftPickForTest(t, ctx, q, current.ID, people["alice"].cur, torch.ID, 1)
	createDraftPickForTest(t, ctx, q, current.ID, people["bob"].cur, torch.ID, 1)
	createDraftPickForTest(t, ctx, q, past.ID, people["alice"].past, pastPick.ID, 1)
	createDraftPickForTest(t, ctx, q, past.ID, outsider, pastPick.ID, 1)

	activity := createActivityForTest(t, ctx, q, current.ID, now.Add(-48*time.Hour), nil, "draft_submission", "Draft submissions")
	occ := createOccurrenceForTest(t, ctx, q, activity.ID, "bonus", "early", now.Add(-24*time.Hour))
	createLedgerEntryForTest(t, ctx, q, current.ID, people["alice"].cur, occ.ID, pgtype.UUID{}, "award", 2, "public", "early", "early-alice")
	createLedgerEntryForTest(t, ctx, q, current.ID, people["alice"].cur, occ.ID, pgtype.UUID{}, "award", 40, "secret", "idol", "secret-alice")

	// Bob's tribe: an event with its challenge points by week.
	toka := createParticipantGroupForTest(t, ctx, q, current.ID, "Toka", "tribe")
	if _, err := pool.Exec(ctx, `INSERT INTO participant_group_membership_periods (participant_group_id, participant_id, starts_at)
		SELECT g.id, p.id, $3 FROM participant_groups g, participants p WHERE g.public_id = $1 AND p.public_id = $2`, toka.ID, people["bob"].cur, now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	tc := createActivityForTest(t, ctx, q, current.ID, now.Add(-72*time.Hour), nil, "tribe_challenge", "Tribe challenges")
	tcOcc := createOccurrenceForTest(t, ctx, q, tc.ID, "immunity", "Toka won immunity", now.Add(-24*time.Hour))
	createLedgerEntryForTest(t, ctx, q, current.ID, people["bob"].cur, tcOcc.ID, pgtype.UUID{}, "award", 2, "public", "Toka won immunity", "tribe_challenge:toka")

	for _, user := range []string{"alice", "bob", "carol", "dave"} {
		hash := sha256.Sum256([]byte(user + "-profile"))
		if _, err := pool.Exec(ctx, `INSERT INTO web_sessions (token_hash, discord_user_id, discord_username, kind, expires_at) VALUES ($1,$2,$2,'browser',$3)`, hash[:], user, now.AddDate(0, 1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	id := func(u pgtype.UUID) string { return uuid.UUID(u.Bytes).String() }
	router := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now }), httpapi.WithPublic(httpapi.PublicConfig{
		BaseURL: "https://castaway.example", InstanceID: id(current.ID), LeagueName: "NowThisIsPodracing", LeagueInstanceIDs: []string{id(past.ID)},
	})).PublicRouter()
	get := func(path, user string, status int) string {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: "castaway_session", Value: user + "-profile"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("GET %s as %s: %d, want %d: %s", path, user, rec.Code, status, rec.Body)
		}
		return rec.Body.String()
	}
	has := func(body string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Fatalf("missing %q in:\n%s", w, body)
			}
		}
	}
	lacks := func(body string, bad ...string) {
		t.Helper()
		for _, b := range bad {
			if strings.Contains(body, b) {
				t.Fatalf("unexpected %q in:\n%s", b, body)
			}
		}
	}

	me := get("/me", "alice", 200)
	has(me, "Season 51</a> · NowThisIsPodracing", "Me · alice", "Torch Bearer", "2 bonus", "Season 50", "Current", "/players/"+id(people["alice"].past))
	lacks(me, "BrainLand", "42<span", "40 bonus", "42 bonus")
	has(get("/", "alice", 200), `href="/players/`+id(people["bob"].cur)+`"`, `href="/me"`)

	// Drafts are soft-closed: while submissions are open, other players' current drafts stay hidden.
	bob := get("/players/"+id(people["alice"].cur), "bob", 200)
	has(bob, "Drafts are revealed after drafts close.", "alice")
	lacks(bob, "Torch Bearer", "Me ·")
	// Nobody ran `draft close` but an episode is scored: the season is underway, so drafts show.
	if _, err := pool.Exec(ctx, `INSERT INTO outcome_positions (instance_id, position, contestant_id)
		SELECT i.id, 99, c.id FROM instances i, contestants c WHERE i.public_id = $1 AND c.public_id = $2`, current.ID, torch.ID); err != nil {
		t.Fatal(err)
	}
	has(get("/players/"+id(people["alice"].cur), "bob", 200), "Torch Bearer")
	has(get("/players/"+id(people["bob"].cur), "alice", 200), "Events", "Tribe Toka", "Week 1: +2", "Previous Seasons")
	if _, err := pool.Exec(ctx, `DELETE FROM outcome_positions op USING instances i WHERE op.instance_id = i.id AND i.public_id = $1`, current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE instance_activities SET status = 'completed' WHERE public_id = $1`, activity.ID); err != nil {
		t.Fatal(err)
	}
	has(get("/players/"+id(people["alice"].cur), "bob", 200), "Torch Bearer")
	has(get("/players/"+id(people["alice"].cur), "carol", 200), "Submit your own draft")
	has(get("/players/"+id(people["alice"].past), "carol", 200), "Old Legend", "Season 50")

	// Scored picks show (+value − distance); eliminations are italic only while the season runs; short names win.
	second := createContestantForTest(t, ctx, q, current.ID, "Second Banana")
	createDraftPickForTest(t, ctx, q, current.ID, people["alice"].cur, second.ID, 2)
	if _, err := pool.Exec(ctx, `UPDATE contestants SET short_name = 'Torchy' WHERE public_id = $1`, torch.ID); err != nil {
		t.Fatal(err)
	}
	has(get("/me", "alice", 200), "Torchy", "Second Banana")
	lacks(get("/me", "alice", 200), "<em>", `small">(+`)
	for _, o := range []struct {
		inst, cont pgtype.UUID
		pos        int
	}{{current.ID, torch.ID, 2}, {past.ID, pastPick.ID, 1}} {
		if _, err := pool.Exec(ctx, `INSERT INTO outcome_positions (instance_id, position, contestant_id)
			SELECT i.id, $3, c.id FROM instances i, contestants c WHERE i.public_id = $1 AND c.public_id = $2`, o.inst, o.cont, o.pos); err != nil {
			t.Fatal(err)
		}
	}
	has(get("/me", "alice", 200), "<em>Torchy</em> <span class=\"muted small\" title=\"(+1 − 1)\">+0</span>", `aria-label="How picks score"`)
	pastProfile := get("/players/"+id(people["alice"].past), "alice", 200)
	has(pastProfile, "Old Legend <span class=\"muted small\" title=\"(+1 − 0)\">+1</span>")
	lacks(pastProfile, "<em>")
	lacks(get("/seasons/"+id(past.ID), "alice", 200), ">Tribe</th>")
	has(get("/players/"+id(people["carol"].cur), "alice", 200), "Unavailable", "No draft on record.")

	// Past league players are viewable; other instances are not. Not a player this season → no Me page.
	get("/players/"+id(people["alice"].other), "bob", 404)
	has(get("/players/"+id(outsider), "alice", 200), "Old Timer", "Old Legend")

	// Seasons: league list with past winners; past standings link players; BrainLand stays out.
	seasons := get("/seasons", "bob", 200)
	has(seasons, "Season 50", "/seasons/"+id(past.ID), "Current")
	lacks(seasons, "BrainLand")
	has(get("/seasons/"+id(past.ID), "bob", 200), "<h1>Season 50</h1>", "/players/"+id(outsider))
	lacks(get("/seasons/"+id(past.ID), "bob", 200), "Torch Bearer")
	get("/seasons/"+id(other.ID), "bob", 404)
	get("/seasons/"+id(current.ID), "bob", 302)
	get("/seasons", "dave", 302)
	// Past standings: trophy only for the winner; Bonus column only when the season had bonuses
	// (this fixture's past bonus is secret, so the column is hidden). Games hub links to Castawordle.
	pastPage := get("/seasons/"+id(past.ID), "bob", 200)
	if !strings.Contains(pastPage, `Alice (S50)</a> <span aria-label="Winner">🏆`) || strings.Contains(pastPage, ">Bonus</th>") {
		t.Fatalf("past standings missing trophy or bonus column: %s", pastPage)
	}
	lacks(get("/", "bob", 200), "🏆")
	has(get("/games", "bob", 200), `<h1>Games</h1>`)
	has(get("/me", "bob", 200), `href="/games">Games</a>`)
	lacks(get("/me", "bob", 200), ">standings<")

	// Pronouns: only the player sees (and edits) their own; saving updates every league season they played.
	post := func(path, body, user, origin string, status int) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		req.AddCookie(&http.Cookie{Name: "castaway_session", Value: user + "-profile"})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("POST %s as %s: %d, want %d: %s", path, user, rec.Code, status, rec.Body)
		}
	}
	post("/me/pronouns", "pronouns=they%2Fthem", "alice", "https://castaway.example", 303)
	post("/me/pronouns", "pronouns=she%2Fher", "alice", "https://evil.example", 403)
	post("/me/pronouns", "pronouns=xe%2Fxem", "alice", "https://castaway.example", 400)
	has(get("/me", "alice", 200), `value="they/them" selected`, `class="pronouns"`)
	for _, path := range []string{"/players/" + id(people["alice"].cur), "/players/" + id(people["alice"].past), "/seasons/" + id(past.ID), "/"} {
		lacks(get(path, "bob", 200), "they/them", `class="pronouns"`, "/me/pronouns")
	}
	var counts [2]int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE pronouns = 'they/them'), count(*) FILTER (WHERE pronouns IS NOT NULL) FROM participants`).Scan(&counts[0], &counts[1]); err != nil || counts != [2]int{2, 2} {
		t.Fatalf("pronouns saved to %v (want alice's 2 league seasons only, not BrainLand): %v", counts, err)
	}
	post("/me/pronouns", "pronouns=", "alice", "https://castaway.example", 303)
	has(get("/me", "alice", 200), `value="" selected`)
	get("/players/not-a-uuid", "alice", 404)
	get("/me", "dave", 302)
}
