package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Guild 1 is @everyone (ID == guild). The bot (9) holds role 20 only.
func botState(roles []permRole, overwrites ...permOverwrite) permState {
	return permState{GuildID: "1", OwnerID: "99", BotID: "9", MemberRoles: []string{"20"},
		Roles: append([]permRole{{ID: "1", Name: "@everyone", Perms: permViewChannel}}, roles...), Overwrites: overwrites}
}

func TestEffectivePerms(t *testing.T) {
	bot := permRole{ID: "20", Name: "Survivor Bot"}
	with := func(perms int64) permRole { r := bot; r.Perms = perms; return r }
	role := func(id string, allow, deny int64) permOverwrite {
		return permOverwrite{ID: id, Allow: allow, Deny: deny}
	}
	member := func(allow, deny int64) permOverwrite {
		return permOverwrite{ID: "9", Member: true, Allow: allow, Deny: deny}
	}
	for _, tc := range []struct {
		name  string
		state permState
		want  int64 // bits that must be set
		deny  int64 // bits that must be clear
	}{
		{"role grants a bit the everyone role lacks", botState([]permRole{with(permSendMessages)}), permViewChannel | permSendMessages, permManageMessages},
		{"administrator bypasses channel denies", botState([]permRole{with(permAdministrator)}, role("1", 0, permSendMessages|permManageMessages)), permManageMessages | permCreatePublicThreads, 0},
		{"channel @everyone deny removes a bit", botState([]permRole{with(permSendMessages)}, role("1", 0, permSendMessages)), permViewChannel, permSendMessages},
		{"role overwrite allow adds a bit", botState(nil, role("20", permManageMessages, 0)), permManageMessages, 0},
		{"role overwrite deny removes a bit", botState([]permRole{with(permManageMessages)}, role("20", 0, permManageMessages)), permViewChannel, permManageMessages},
		{"member overwrite wins over role deny", botState([]permRole{with(permSendMessages)}, role("20", 0, permSendMessages), member(permSendMessages, 0)), permSendMessages, 0},
		{"member overwrite deny beats role allow", botState([]permRole{with(0)}, role("20", permCreatePublicThreads, 0), member(0, permCreatePublicThreads)), 0, permCreatePublicThreads},
		{"overwrite for a role the bot lacks is ignored", botState([]permRole{with(0)}, role("55", 0, permSendMessages)), permViewChannel, 0},
		{"owner has everything", permState{GuildID: "1", OwnerID: "9", BotID: "9", Overwrites: []permOverwrite{role("1", 0, permSendMessages)}}, permSendMessages, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := effectivePerms(tc.state)
			if got&tc.want != tc.want || got&tc.deny != 0 {
				t.Fatalf("perms %b: want set %b, clear %b", got, tc.want, tc.deny)
			}
		})
	}
}

func TestPermSourcesNameRolesAndOverwrites(t *testing.T) {
	s := botState([]permRole{{ID: "20", Name: "Survivor Bot", Perms: permManageMessages}},
		permOverwrite{ID: "20", Deny: permSendMessages}, permOverwrite{ID: "1", Allow: permCreatePublicThreads})
	grants, denies := permSources(s, permManageMessages)
	if strings.Join(grants, ",") != "Survivor Bot" || len(denies) != 0 {
		t.Fatalf("manage messages: grants %v denies %v", grants, denies)
	}
	grants, denies = permSources(s, permSendMessages)
	if len(grants) != 0 || strings.Join(denies, ",") != "overwrite Survivor Bot" {
		t.Fatalf("send messages: grants %v denies %v", grants, denies)
	}
}

// The command reads bindings from the API and verdicts from Discord's REST data (no gateway). It reports, not fails.
func TestBotPermissionsCommand(t *testing.T) {
	t.Setenv("PROBST_TOKEN", "test-secret")
	t.Setenv("PROBST_DISCORD_USER_ID", "123")
	t.Setenv("CASTAWAY_DISCORD_BOT_TOKEN", "bot-secret")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/instances/inst/discord-channels" {
			t.Errorf("unexpected API %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"channels": []map[string]string{{"guild_id": "1", "channel_id": "100"}, {"guild_id": "1", "channel_id": "101"}}})
	}))
	defer api.Close()
	// @everyone: View, Send, Read History. Bot role: Manage Messages, Create Public Threads (1<<35).
	// Channel 101 denies Send Messages for @everyone.
	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot bot-secret" {
			t.Errorf("bad auth on %s", r.URL.Path)
		}
		body := map[string]any{
			"/users/@me":          map[string]any{"id": "9"},
			"/guilds/1":           map[string]any{"name": "Brain Land", "owner_id": "99"},
			"/guilds/1/roles":     []map[string]any{{"id": "1", "name": "@everyone", "permissions": "68608"}, {"id": "20", "name": "Survivor Bot", "permissions": "34359746560"}},
			"/guilds/1/members/9": map[string]any{"roles": []string{"20"}},
			"/channels/100":       map[string]any{"name": "season-51", "guild_id": "1", "permission_overwrites": []map[string]any{}},
			"/channels/101": map[string]any{"name": "season-52", "guild_id": "1", "permission_overwrites": []map[string]any{
				{"id": "1", "type": 0, "allow": "0", "deny": "2048"}}},
		}[r.URL.Path]
		if body == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer discord.Close()
	t.Setenv("PROBST_API_URL", api.URL)
	t.Setenv("PROBST_DISCORD_API_URL", discord.URL)

	out, err := runProbst(t, "bot", "permissions", "--instance", "inst")
	if err != nil {
		t.Fatalf("report should succeed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Brain Land — #season-51 (100)",
		"  ✓ Create Public Threads — granted by Survivor Bot",
		"  ✗ Send Messages in Threads\n",
		"  missing: Send Messages in Threads\n",
		"Brain Land — #season-52 (101)",
		"  ✗ Send Messages — denied by overwrite @everyone",
		"  missing: Send Messages, Send Messages in Threads\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestBotPermissionsNeedsInstanceAndToken(t *testing.T) {
	if _, err := runProbst(t, "bot", "permissions"); err == nil || !strings.Contains(err.Error(), "--instance is required") {
		t.Fatalf("missing instance: %v", err)
	}
	t.Setenv("CASTAWAY_DISCORD_BOT_TOKEN", "")
	t.Setenv("PROBST_TOKEN", "test-secret")
	t.Setenv("PROBST_DISCORD_USER_ID", "123")
	if _, err := runProbst(t, "bot", "permissions", "--instance", "inst"); err == nil || !strings.Contains(err.Error(), "CASTAWAY_DISCORD_BOT_TOKEN") {
		t.Fatalf("missing token: %v", err)
	}
}
