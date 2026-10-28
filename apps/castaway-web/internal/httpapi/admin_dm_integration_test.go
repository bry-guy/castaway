package httpapi_test

import (
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

// Admin DM actions: an approval-gated post waits for an admin's "yes"; an admin's corrected draft saves as
// the author's draft, and non-admins can do neither.
func TestAdminDMActions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	queries := db.New(pool)
	instance := createInstanceForTest(t, ctx, queries, "Admin DMs", 954)
	instanceID := uuid.UUID(instance.ID.Bytes).String()
	path := "/instances/" + instanceID
	for _, name := range []string{"Ann Alpha", "Thien \"An\" Nguyen", "Cyd Cole"} {
		createContestantForTest(t, ctx, queries, instance.ID, name)
	}
	player := createParticipantForTest(t, ctx, queries, instance.ID, "P1")
	if _, err := pool.Exec(ctx, `UPDATE participants SET discord_user_id = '1000' WHERE public_id = $1`, player.ID); err != nil {
		t.Fatal(err)
	}
	for _, admin := range []string{"2001", "2002"} {
		if _, err := queries.CreateInstanceAdmin(ctx, db.CreateInstanceAdminParams{InstanceID: instance.ID, DiscordUserID: admin}); err != nil {
			t.Fatal(err)
		}
	}
	if err := queries.SetDiscordChannelBinding(ctx, db.SetDiscordChannelBindingParams{GuildID: "7001", ChannelID: "7002", InstanceID: instance.ID}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)
	router := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now }), httpapi.WithServiceAuth(httpapi.ServiceAuthConfig{Enabled: true, BearerTokens: []string{"token"}})).Router()
	type reply struct {
		Announcement *struct {
			ID string `json:"id"`
		} `json:"announcement"`
		Revision    string   `json:"revision"`
		Missed      bool     `json:"missed"`
		Admins      []string `json:"admin_discord_user_ids"`
		ScheduledAt string   `json:"scheduled_at"`
		Status      string   `json:"status"`
		SavedPicks  string   `json:"saved_picks"`
	}
	// listed is the instance's announcement list, which shows the approval state.
	type listed struct {
		Announcements []struct {
			Status             string  `json:"status"`
			ApprovalGated      bool    `json:"approval_gated"`
			ApprovalSendAt     *string `json:"approval_send_at"`
			ApprovalNotifiedAt *string `json:"approval_notified_at"`
		} `json:"announcements"`
	}
	list := func() listed {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, authorizedJSONRequest(http.MethodGet, path+"/announcements", "", "token", "2001"))
		var out listed
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &out) != nil || len(out.Announcements) != 1 {
			t.Fatalf("list: %d %s", recorder.Code, recorder.Body.String())
		}
		return out
	}
	call := func(method, url, body, actor string, want int) reply {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, authorizedJSONRequest(method, url, body, "token", actor))
		if recorder.Code != want {
			t.Fatalf("%s %s: %d %s", method, url, recorder.Code, recorder.Body.String())
		}
		var out reply
		if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Approval gate.
	created := call(http.MethodPost, path+"/announcements", `{"guild_id":"7001","channel_id":"7002","request_key":"w2","body":"Week 2 scores","draft":true}`, "2001", http.StatusOK)
	id := created.Announcement.ID
	call(http.MethodPut, path+"/announcements/"+id+"/approval", `{"send_at":"2026-10-07T16:00:00Z"}`, "2001", http.StatusOK)
	call(http.MethodPut, path+"/announcements/"+id+"/approval", `{"send_at":"2026-10-07T03:00:00Z"}`, "2001", http.StatusBadRequest)    // past
	call(http.MethodPut, path+"/announcements/"+id+"/schedule", `{"scheduled_at":"2026-10-07T16:00:00Z"}`, "2001", http.StatusConflict) // can't bypass the gate
	claimPath, claimBody := "/announcements/approvals/claim", `{"guild_ids":["7001"]}`
	claim := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK)
	if claim.Announcement == nil || fmt.Sprint(claim.Admins) != "[2001 2002]" {
		t.Fatalf("approval claim: %v", claim)
	}
	oldRevision := claim.Revision
	if again := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK); again.Announcement != nil {
		t.Fatalf("approval DM claimed twice within its lease: %v", again)
	}
	now = now.Add(11 * time.Minute) // undelivered: offered again after the lease
	if retry := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK); retry.Revision != oldRevision {
		t.Fatalf("undelivered approval DM should be retried: %v", retry)
	}
	call(http.MethodPost, "/announcements/"+id+"/approval-delivered", fmt.Sprintf(`{"revision":%q}`, oldRevision), "", http.StatusOK)
	if got := list().Announcements[0]; got.Status != "draft" || !got.ApprovalGated || got.ApprovalSendAt == nil || *got.ApprovalSendAt != "2026-10-07T16:00:00Z" || got.ApprovalNotifiedAt == nil {
		t.Fatalf("list after the approval DM: %+v", got)
	}
	now = now.Add(11 * time.Minute)
	if again := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK); again.Announcement != nil {
		t.Fatalf("delivered approval DM was offered again: %v", again)
	}
	if got := call(http.MethodPost, "/announcements/claim", `{"guild_ids":["7001"]}`, "", http.StatusOK); got.Announcement != nil {
		t.Fatal("an unapproved post was claimed for sending")
	}
	call(http.MethodPut, path+"/announcements/"+id+"/body", `{"body":"Week 2 scores, fixed"}`, "2001", http.StatusOK)
	again := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK)
	if again.Announcement == nil || again.Revision == oldRevision {
		t.Fatalf("edited copy should be re-sent for approval as a new revision: %v", again)
	}
	revision := again.Revision
	approve := func(admin, rev string, want int) reply {
		return call(http.MethodPost, "/announcements/"+id+"/approve", fmt.Sprintf(`{"admin_discord_user_id":%q,"revision":%q}`, admin, rev), "", want)
	}
	// An admin's DM reply with new text replaces it and re-asks; the replaced revision can't be approved.
	revise := func(admin, rev, body string, want int) {
		call(http.MethodPost, "/announcements/"+id+"/revise", fmt.Sprintf(`{"admin_discord_user_id":%q,"revision":%q,"body":%q}`, admin, rev, body), "", want)
	}
	revise("1000", revision, "Player copy", http.StatusConflict)   // not an admin
	revise("2001", oldRevision, "Stale copy", http.StatusConflict) // a reply to an old DM
	revise("2001", revision, "   ", http.StatusBadRequest)         // blank
	revise("2001", revision, "Week 2 scores, by DM", http.StatusOK)
	revise("2002", revision, "Week 2 scores, raced", http.StatusConflict) // the DM it answered is now stale
	approve("2002", revision, http.StatusConflict)
	byDM := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK)
	if byDM.Announcement == nil || byDM.Revision == revision {
		t.Fatalf("revised copy should be re-sent for approval: %v", byDM)
	}
	var body string
	if err := pool.QueryRow(ctx, `SELECT body FROM announcements WHERE id = $1`, id).Scan(&body); err != nil || body != "Week 2 scores, by DM" {
		t.Fatalf("revised body = %q (%v)", body, err)
	}
	revision = byDM.Revision
	approve("2002", oldRevision, http.StatusConflict) // a reply to the old DM can't approve the new text
	approve("1000", revision, http.StatusConflict)    // not an admin
	if approved := approve("2002", revision, http.StatusOK); approved.ScheduledAt != "2026-10-07T16:00:00Z" {
		t.Fatalf("approved: %v", approved)
	}
	approve("2001", revision, http.StatusConflict) // once
	if got := list().Announcements[0]; got.Status != "pending" || !got.ApprovalGated {
		t.Fatalf("list after approval: %+v", got)
	}

	// Editing after approval holds it again and re-asks.
	call(http.MethodPut, path+"/announcements/"+id+"/body", `{"body":"Week 2 scores, fixed again"}`, "2001", http.StatusOK)
	if got := call(http.MethodPost, "/announcements/claim", `{"guild_ids":["7001"]}`, "", http.StatusOK); got.Announcement != nil {
		t.Fatal("an edited, unapproved post was claimed for sending")
	}
	revision = call(http.MethodPost, claimPath, claimBody, "", http.StatusOK).Revision
	approve("2001", revision, http.StatusOK)
	if got := call(http.MethodPost, "/announcements/claim", `{"guild_ids":["7001"]}`, "", http.StatusOK); got.Announcement != nil {
		t.Fatal("approved post sent before its time")
	}
	now = time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)
	if got := call(http.MethodPost, "/announcements/claim", `{"guild_ids":["7001"]}`, "", http.StatusOK); got.Announcement == nil {
		t.Fatal("approved post didn't send at its time")
	}

	// Approved after its time (within the late window), it posts right away; never before approval.
	late := call(http.MethodPost, path+"/announcements", `{"guild_id":"7001","channel_id":"7002","request_key":"late","body":"Late post","draft":true}`, "2001", http.StatusOK)
	lateID := late.Announcement.ID
	call(http.MethodPut, path+"/announcements/"+lateID+"/approval", `{"send_at":"2026-10-07T17:00:00Z"}`, "2001", http.StatusOK)
	lateRevision := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK).Revision
	delivered := `{"revision":"` + lateRevision + `"}`
	call(http.MethodPost, "/announcements/"+lateID+"/approval-delivered", delivered, "", http.StatusOK)
	if again := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK); again.Announcement != nil {
		t.Fatal("asked twice before its time")
	}
	now = time.Date(2026, 10, 7, 17, 1, 0, 0, time.UTC)
	if got := call(http.MethodPost, "/announcements/claim", `{"guild_ids":["7001"]}`, "", http.StatusOK); got.Announcement != nil {
		t.Fatal("a late, unapproved post was sent")
	}
	// Its time passed unapproved: one "missed" DM, for the same revision.
	if missed := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK); !missed.Missed || missed.Revision != lateRevision {
		t.Fatalf("missed-time DM: %+v", missed)
	}
	call(http.MethodPost, "/announcements/"+lateID+"/approval-delivered", delivered, "", http.StatusOK)
	if again := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK); again.Announcement != nil {
		t.Fatal("missed DM sent twice")
	}
	// Replying with new text to the missed DM re-asks once (not as "missed"); approving that posts now.
	call(http.MethodPost, "/announcements/"+lateID+"/revise", fmt.Sprintf(`{"admin_discord_user_id":"2001","revision":%q,"body":"Late post, fixed"}`, lateRevision), "", http.StatusOK)
	reask := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK)
	if reask.Missed || reask.Revision == lateRevision {
		t.Fatalf("re-ask after a late edit: %+v", reask)
	}
	call(http.MethodPost, "/announcements/"+lateID+"/approval-delivered", `{"revision":"`+reask.Revision+`"}`, "", http.StatusOK)
	if again := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK); again.Announcement != nil {
		t.Fatal("a late edit got a second missed DM")
	}
	lateRevision = reask.Revision
	if approved := call(http.MethodPost, "/announcements/"+lateID+"/approve", fmt.Sprintf(`{"admin_discord_user_id":"2001","revision":%q}`, lateRevision), "", http.StatusOK); approved.ScheduledAt != "2026-10-07T17:01:00Z" {
		t.Fatalf("late approval should post now: %v", approved)
	}
	if got := call(http.MethodPost, "/announcements/claim", `{"guild_ids":["7001"]}`, "", http.StatusOK); got.Announcement == nil {
		t.Fatal("a late-approved post didn't send")
	}
	// Past the late window it's stale: it can't be approved.
	stale := call(http.MethodPost, path+"/announcements", `{"guild_id":"7001","channel_id":"7002","request_key":"stale","body":"Stale post","draft":true}`, "2001", http.StatusOK)
	call(http.MethodPut, path+"/announcements/"+stale.Announcement.ID+"/approval", `{"send_at":"2026-10-07T17:30:00Z"}`, "2001", http.StatusOK)
	staleRevision := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK).Revision
	now = time.Date(2026, 10, 14, 17, 31, 0, 0, time.UTC) // more than 7 days late
	call(http.MethodPost, "/announcements/"+stale.Announcement.ID+"/approve", fmt.Sprintf(`{"admin_discord_user_id":"2001","revision":%q}`, staleRevision), "", http.StatusConflict)
	now = time.Date(2026, 10, 7, 17, 1, 0, 0, time.UTC)

	// An approved post whose time passed while the bot was down expires instead of sending late.
	down := call(http.MethodPost, path+"/announcements", `{"guild_id":"7001","channel_id":"7002","request_key":"down","body":"Down post","draft":true}`, "2001", http.StatusOK)
	call(http.MethodPut, path+"/announcements/"+down.Announcement.ID+"/approval", `{"send_at":"2026-10-07T18:00:00Z"}`, "2001", http.StatusOK)
	downRevision := call(http.MethodPost, claimPath, claimBody, "", http.StatusOK).Revision
	call(http.MethodPost, "/announcements/"+down.Announcement.ID+"/approve", fmt.Sprintf(`{"admin_discord_user_id":"2001","revision":%q}`, downRevision), "", http.StatusOK)
	now = time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)
	if got := call(http.MethodPost, "/announcements/claim", `{"guild_ids":["7001"]}`, "", http.StatusOK); got.Announcement != nil {
		t.Fatal("an approved post was sent an hour late")
	}

	// Admin draft fixes.
	call(http.MethodPost, path+"/draft-submissions", `{"tribes":["Savu","Toka"],"guild_id":"7001","channel_id":"7002"}`, "2001", http.StatusCreated)
	call(http.MethodPut, path+"/draft-submissions/thread", `{"thread_id":"8001"}`, "2001", http.StatusOK)
	overwrite := ""
	post := func(version, content, fixedBy string, want int) reply {
		body, err := json.Marshal(map[string]string{"message_id": "9001", "version": version, "author_discord_user_id": "1000", "content": content, "fixed_by_discord_user_id": fixedBy, "overwrite_picks": overwrite})
		if err != nil {
			t.Fatal(err)
		}
		return call(http.MethodPost, "/draft-threads/8001/messages", string(body), "", want)
	}
	problem := post("v1", "1. Ann\n2. Ann\n3. Cyd", "", http.StatusOK)
	if problem.Status != "problem" || fmt.Sprint(problem.Admins) != "[2001 2002]" {
		t.Fatalf("problem: %v", problem)
	}
	post("fix-1", "1. Ann\n2. Thien An\n3. Cyd", "1000", http.StatusForbidden) // the player can't "fix" as admin
	if saved := post("fix-2", "1. Ann\n2. Thien An\n3. Cyd", "2002", http.StatusOK); saved.Status != "saved" {
		t.Fatalf("admin fix: %v", saved)
	}
	// A later fix (another admin's, or after the player saved through any post) doesn't overwrite saved picks.
	// It's offered as a conflict with the saved draft's fingerprint; overwriting needs that exact fingerprint.
	conflict := post("fix-3", "1. Cyd\n2. Thien An\n3. Ann", "2001", http.StatusOK)
	if conflict.Status != "conflict" || len(conflict.SavedPicks) != 32 {
		t.Fatalf("expected a conflict with a fingerprint: %v", conflict)
	}
	overwrite = strings.Repeat("0", 32) // a stale fingerprint (the draft changed since the DM)
	if stale := post("fix-4", "1. Cyd\n2. Thien An\n3. Ann", "2001", http.StatusOK); stale.Status != "conflict" {
		t.Fatalf("stale overwrite should be refused: %v", stale)
	}
	overwrite = conflict.SavedPicks
	if replaced := post("fix-5", "1. Cyd\n2. Thien An\n3. Ann", "2001", http.StatusOK); replaced.Status != "saved" {
		t.Fatalf("overwrite: %v", replaced)
	}
	overwrite = conflict.SavedPicks // now stale: the replaced draft is different
	if again := post("fix-6", "1. Ann\n2. Thien An\n3. Cyd", "2002", http.StatusOK); again.Status != "conflict" {
		t.Fatalf("a second admin's overwrite of the old draft should be refused: %v", again)
	}
	var picks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM draft_picks d JOIN participants p ON p.id = d.participant_id WHERE p.public_id = $1`, player.ID).Scan(&picks); err != nil || picks != 3 {
		t.Fatalf("saved picks = %d (%v), want 3", picks, err)
	}
}
