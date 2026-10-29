package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/httpapi"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Episode discussion threads: queued once per key, claimed when due, sent at most once, failed (never retried)
// when a claim is interrupted or Discord refused, and re-queued only when no thread was opened.
func TestDiscussionThreads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	queries := db.New(pool)
	instance := createInstanceForTest(t, ctx, queries, "BrainLand", 51)
	instanceID := uuid.UUID(instance.ID.Bytes).String()
	if _, err := queries.CreateInstanceAdmin(ctx, db.CreateInstanceAdminParams{InstanceID: instance.ID, DiscordUserID: "2001"}); err != nil {
		t.Fatal(err)
	}
	if err := queries.SetDiscordChannelBinding(ctx, db.SetDiscordChannelBindingParams{GuildID: "7001", ChannelID: "7002", InstanceID: instance.ID}); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 28, 23, 0, 0, 0, time.UTC)
	router := httpapi.New(pool, httpapi.WithClock(func() time.Time { return clock }), httpapi.WithServiceAuth(httpapi.ServiceAuthConfig{Enabled: true, BearerTokens: []string{"token"}})).Router()
	do := func(method, path, body string, want int) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, authorizedJSONRequest(method, path, body, "token", "2001"))
		if recorder.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, recorder.Code, want, recorder.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	queue := func(key, channel, name string) string {
		t.Helper()
		out := do(http.MethodPut, "/instances/"+instanceID+"/discussion-threads/"+key,
			`{"guild_id":"7001","channel_id":"`+channel+`","name":"`+name+`","due_at":"2026-10-28T23:00:00Z"}`, http.StatusOK)
		action, _ := out["action"].(string)
		return action
	}
	claim := func() map[string]any {
		t.Helper()
		return do(http.MethodPost, "/discussion-threads/claim", `{"guild_ids":["7001"]}`, http.StatusOK)
	}
	finish := func(id, body string) {
		t.Helper()
		do(http.MethodPost, "/discussion-threads/"+id+"/finish", body, http.StatusOK)
	}
	claimedID := func(out map[string]any) string {
		t.Helper()
		d, _ := out["discussion"].(map[string]any)
		id, _ := d["id"].(string)
		return id
	}

	if got := queue("s51-ep6-discussion", "7002", "Survivor S51 E6 Discussion"); got != "created" {
		t.Fatalf("first queue: %s", got)
	}
	if got := queue("s51-ep6-discussion", "7002", "Survivor S51 E6 Discussion"); got != "unchanged" {
		t.Fatalf("same key again must not queue twice: %s", got)
	}
	do(http.MethodPut, "/instances/"+instanceID+"/discussion-threads/s51-ep6-discussion",
		`{"guild_id":"7001","channel_id":"9999","name":"Survivor S51 E6 Discussion","due_at":"2026-10-28T23:00:00Z"}`, http.StatusConflict)

	// Not due yet: nothing to claim.
	clock = clock.Add(-time.Hour)
	if out := claim(); out["discussion"] != nil {
		t.Fatalf("claimed before due: %v", out)
	}
	clock = clock.Add(time.Hour)

	first := claim()
	id := claimedID(first)
	if id == "" || first["admin_discord_user_ids"] == nil {
		t.Fatalf("claim: %v", first)
	}
	if again := claim(); claimedID(again) != "" {
		t.Fatalf("a claimed thread was offered again: %v", again)
	}
	// The bot died mid-claim: after the lease the row fails (no retry) rather than opening a second thread.
	clock = clock.Add(11 * time.Minute)
	if out := claim(); claimedID(out) != "" {
		t.Fatalf("interrupted thread retried: %v", out)
	}
	if got := queue("s51-ep6-discussion", "7002", "Survivor S51 E6 Discussion"); got != "unchanged" {
		t.Fatalf("interrupted thread may already exist; it must not be queued again, got %s", got)
	}

	// Sent: a finished thread with its line is terminal.
	if got := queue("s51-ep7-discussion", "7002", "Survivor S51 E7 Discussion"); got != "created" {
		t.Fatalf("ep7: %s", got)
	}
	sentID := claimedID(claim())
	finish(sentID, `{"thread_id":"9001","line_message_id":"9002"}`)
	if got := queue("s51-ep7-discussion", "7002", "Survivor S51 E7 Discussion"); got != "unchanged" {
		t.Fatalf("sent thread queued again: %s", got)
	}

	// Failed with a thread opened: never re-queued (a second thread would duplicate it).
	if got := queue("s51-ep8-discussion", "7002", "Survivor S51 E8 Discussion"); got != "created" {
		t.Fatalf("ep8: %s", got)
	}
	failedWithThread := claimedID(claim())
	finish(failedWithThread, `{"thread_id":"9100","failure":"couldn't post the line"}`)
	if got := queue("s51-ep8-discussion", "7002", "Survivor S51 E8 Discussion"); got != "unchanged" {
		t.Fatalf("failed thread that was opened got %s", got)
	}

	// Failed before any thread (missing permission): a fresh queue opens it again.
	if got := queue("s51-ep9-discussion", "7002", "Survivor S51 E9 Discussion"); got != "created" {
		t.Fatalf("ep9: %s", got)
	}
	finish(claimedID(claim()), `{"failure":"missing permission: Manage Messages"}`)
	if got := queue("s51-ep9-discussion", "7002", "Survivor S51 E9 Discussion"); got != "requeued" {
		t.Fatalf("failed with no thread should requeue: %s", got)
	}
	if claimedID(claim()) == "" {
		t.Fatal("requeued thread not offered")
	}
	// A finish for a row that isn't being sent is refused.
	do(http.MethodPost, "/discussion-threads/"+sentID+"/finish", `{"thread_id":"9001","line_message_id":"9002"}`, http.StatusConflict)
}
