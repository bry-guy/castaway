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
)

// An admin's free-form DM is a publish request: the reply names the server(s) to post to. Single, multiple,
// unknown, cancel, ambiguous-by-name, and non-admin senders are covered.
func TestAdminPublishRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, pool := integrationPool(t)
	defer pool.Close()
	resetDatabase(t, ctx, pool)
	queries := db.New(pool)
	podracing := createInstanceForTest(t, ctx, queries, "Podracing", 1)
	brainland := createInstanceForTest(t, ctx, queries, "BrainLand", 51)
	admin := func(inst db.CreateInstanceRow, discordID string) {
		if _, err := queries.CreateInstanceAdmin(ctx, db.CreateInstanceAdminParams{InstanceID: inst.ID, DiscordUserID: discordID}); err != nil {
			t.Fatal(err)
		}
	}
	admin(podracing, "2001")
	admin(brainland, "2001")
	admin(podracing, "2002")
	bind := func(inst db.CreateInstanceRow, guild, channel string) {
		if err := queries.SetDiscordChannelBinding(ctx, db.SetDiscordChannelBindingParams{GuildID: guild, ChannelID: channel, InstanceID: inst.ID}); err != nil {
			t.Fatal(err)
		}
	}
	bind(podracing, "7001", "7002")
	bind(brainland, "7003", "7004")
	now := time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)
	router := httpapi.New(pool, httpapi.WithClock(func() time.Time { return now }), httpapi.WithServiceAuth(httpapi.ServiceAuthConfig{Enabled: true, BearerTokens: []string{"token"}})).Router()
	call := func(path, body string, want int) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, authorizedJSONRequest(http.MethodPost, path, body, "token", ""))
		if recorder.Code != want {
			t.Fatalf("POST %s: got %d want %d: %s", path, recorder.Code, want, recorder.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	create := func(admin, key, text string) (string, []any) {
		t.Helper()
		out := call("/admin-dms/publish-requests", fmt.Sprintf(`{"admin_discord_user_id":%q,"guild_ids":["7001","7003"],"request_key":%q,"body":%q}`, admin, key, text), http.StatusOK)
		return stringField(out, "request_id"), listField(out, "targets")
	}
	reply := func(id, admin, text string) map[string]any {
		t.Helper()
		return call("/admin-dms/publish-requests/"+id+"/reply", fmt.Sprintf(`{"admin_discord_user_id":%q,"guild_ids":["7001","7003"],"reply":%q}`, admin, text), http.StatusOK)
	}
	queued := func(text string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT i.name, a.channel_id FROM announcements a JOIN instances i ON i.id = a.instance_id
			WHERE a.body = $1 AND a.status = 'pending' AND a.due_at <= $2 AND a.notify_users = false ORDER BY i.name`, text, now)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var name, channel string
			if err := rows.Scan(&name, &channel); err != nil {
				t.Fatal(err)
			}
			out = append(out, name+"@"+channel)
		}
		return out
	}

	t.Run("non-admin records nothing", func(t *testing.T) {
		id, targets := create("3001", "900", "Hello")
		if id != "" || len(targets) != 0 {
			t.Fatalf("non-admin got request %q targets %v", id, targets)
		}
	})

	t.Run("unknown name keeps the request open; cancel closes it", func(t *testing.T) {
		id, targets := create("2001", "101", "Hello everyone")
		if id == "" || len(targets) != 2 {
			t.Fatalf("request %q targets %v", id, targets)
		}
		out := reply(id, "2001", "Survivor")
		if out["status"] != "unknown" || strings.Join(toStrings(out["unknown"]), ",") != "Survivor" {
			t.Fatalf("unknown: %v", out)
		}
		if got := queued("Hello everyone"); len(got) != 0 {
			t.Fatalf("unknown name queued %v", got)
		}
		if out := reply(id, "2001", "CANCEL"); out["status"] != "cancelled" {
			t.Fatalf("cancel: %v", out)
		}
		if out := reply(id, "2001", "Podracing"); out["status"] != "closed" {
			t.Fatalf("reply after cancel: %v", out)
		}
		if got := queued("Hello everyone"); len(got) != 0 {
			t.Fatalf("cancelled request queued %v", got)
		}
	})

	t.Run("single server", func(t *testing.T) {
		id, _ := create("2001", "102", "Just podracing")
		out := reply(id, "2001", " podRACING ")
		if out["status"] != "posted" || len(listField(out, "posted")) != 1 {
			t.Fatalf("single: %v", out)
		}
		if got := queued("Just podracing"); strings.Join(got, ",") != "Podracing@7002" {
			t.Fatalf("single queued %v", got)
		}
	})

	t.Run("multiple servers separated by slash", func(t *testing.T) {
		id, _ := create("2001", "103", "Both servers")
		out := reply(id, "2001", "Podracing/BrainLand")
		if out["status"] != "posted" || len(listField(out, "posted")) != 2 {
			t.Fatalf("multi: %v", out)
		}
		if got := queued("Both servers"); strings.Join(got, ",") != "BrainLand@7004,Podracing@7002" {
			t.Fatalf("multi queued %v", got)
		}
	})

	t.Run("second reply after posting is closed, nothing duplicated", func(t *testing.T) {
		id, _ := create("2001", "104", "Once only")
		reply(id, "2001", "BrainLand")
		if out := reply(id, "2001", "BrainLand"); out["status"] != "closed" {
			t.Fatalf("second reply: %v", out)
		}
		if got := queued("Once only"); strings.Join(got, ",") != "BrainLand@7004" {
			t.Fatalf("once queued %v", got)
		}
	})

	t.Run("a name shared by two channels is ambiguous; the channel ID picks one", func(t *testing.T) {
		second := createInstanceForTest(t, ctx, queries, "Podracing", 2)
		admin(second, "2001")
		bind(second, "7003", "7008")
		id, _ := create("2001", "106", "Which one")
		if out := reply(id, "2001", "Podracing"); out["status"] != "ambiguous" {
			t.Fatalf("ambiguous: %v", out)
		}
		if got := queued("Which one"); len(got) != 0 {
			t.Fatalf("ambiguous queued %v", got)
		}
		if out := reply(id, "2001", "7008"); out["status"] != "posted" {
			t.Fatalf("by channel id: %v", out)
		}
		if got := queued("Which one"); strings.Join(got, ",") != "Podracing@7008" {
			t.Fatalf("by channel queued %v", got)
		}
	})

	t.Run("another admin cannot reply to someone else's request", func(t *testing.T) {
		id, _ := create("2001", "105", "Private")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, authorizedJSONRequest(http.MethodPost, "/admin-dms/publish-requests/"+id+"/reply",
			`{"admin_discord_user_id":"2002","guild_ids":["7001","7003"],"reply":"Podracing"}`, "token", ""))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("other admin: %d %s", recorder.Code, recorder.Body.String())
		}
	})
}

func stringField(m map[string]any, key string) string {
	v, ok := m[key].(string)
	if !ok {
		return ""
	}
	return v
}

func listField(m map[string]any, key string) []any {
	items, ok := m[key].([]any)
	if !ok {
		return nil
	}
	return items
}

func toStrings(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
