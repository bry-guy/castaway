package discord

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bry-guy/srvivor/apps/castaway-discord-bot/internal/castaway"
	"github.com/bwmarrin/discordgo"
)

// Episode discussion thread delivery: thread (no opener), system message removed, one line; failures are reported
// to admins and recorded, and no thread is opened without the permissions it needs.
func TestDiscussionThreadDelivery(t *testing.T) {
	all := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionCreatePublicThreads | discordgo.PermissionManageMessages)
	for _, tc := range []struct {
		name        string
		perms       int64
		threadFail  bool
		wantThreads int // threads opened (and their system message removed)
		wantLine    bool
		wantFailure string // substring of the failure recorded, "" for success
		wantDM      bool
	}{
		{name: "opens the thread and posts the line", perms: all, wantThreads: 1, wantLine: true},
		{name: "missing Manage Messages opens nothing and tells admins", perms: all &^ discordgo.PermissionManageMessages, wantFailure: "Manage Messages", wantDM: true},
		{name: "missing Create Public Threads opens nothing", perms: all &^ discordgo.PermissionCreatePublicThreads, wantFailure: "Create Public Threads", wantDM: true},
		{name: "a refused thread is reported", perms: all, threadFail: true, wantFailure: "opening the thread", wantDM: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var finish struct {
				ThreadID      string `json:"thread_id"`
				LineMessageID string `json:"line_message_id"`
				Failure       string `json:"failure"`
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/discussion-threads/claim":
					mustWrite(t, w, `{"discussion":{"id":"d1","instance_id":"i","guild_id":"101","channel_id":"7002","name":"Survivor S51 E6 Discussion"},"admin_discord_user_ids":["2001"]}`)
				case "/discord/guilds/101/channels/7002":
					mustWrite(t, w, `{"binding":{"instance_id":"i"}}`)
				case "/discussion-threads/d1/finish":
					if err := json.NewDecoder(r.Body).Decode(&finish); err != nil {
						t.Error(err)
					}
					mustWrite(t, w, `{"status":"ok"}`)
				default:
					t.Errorf("unexpected API request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			client, err := castaway.NewClient(api.URL, api.Client(), castaway.Options{BearerToken: "test-service"})
			if err != nil {
				t.Fatal(err)
			}

			session, err := discordgo.New("Bot test-token")
			if err != nil {
				t.Fatal(err)
			}
			session.State.User = &discordgo.User{ID: "999", Bot: true}
			if err := session.State.GuildAdd(&discordgo.Guild{ID: "101", OwnerID: "1", Roles: []*discordgo.Role{{ID: "101", Permissions: tc.perms}}}); err != nil {
				t.Fatal(err)
			}
			if err := session.State.ChannelAdd(&discordgo.Channel{ID: "7002", GuildID: "101", Type: discordgo.ChannelTypeGuildText}); err != nil {
				t.Fatal(err)
			}
			if err := session.State.MemberAdd(&discordgo.Member{GuildID: "101", User: &discordgo.User{ID: "999", Bot: true}}); err != nil {
				t.Fatal(err)
			}
			var threads, deletes, lines int
			var dms []string
			session.Client.Transport = announcementRoundTrip(func(r *http.Request) (*http.Response, error) {
				path := r.URL.Path
				switch {
				case strings.HasSuffix(path, "/channels/7002/threads") && r.Method == http.MethodPost:
					if tc.threadFail {
						return discordResponse(r, 403, `{"message":"Missing Permissions","code":50013}`), nil
					}
					threads++
					return discordResponse(r, 200, `{"id":"9001","type":11,"name":"Survivor S51 E6 Discussion"}`), nil
				case strings.HasSuffix(path, "/channels/7002/messages") && r.Method == http.MethodGet:
					// The thread's "started a thread" system message, which the bot must delete.
					return discordResponse(r, 200, `[{"id":"9001","type":18,"content":"","channel_id":"7002"}]`), nil
				case strings.HasSuffix(path, "/channels/7002/messages/9001") && r.Method == http.MethodDelete:
					deletes++
					return discordResponse(r, 204, ``), nil
				case strings.HasSuffix(path, "/channels/7002/messages") && r.Method == http.MethodPost:
					lines++
					var body struct {
						Content         string `json:"content"`
						AllowedMentions struct {
							Parse []string `json:"parse"`
						} `json:"allowed_mentions"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.Content != "<#9001>" || len(body.AllowedMentions.Parse) != 0 {
						t.Errorf("line must be plain and ping nobody: %+v", body)
					}
					return discordResponse(r, 200, `{"id":"9002","channel_id":"7002","content":"x"}`), nil
				case strings.HasSuffix(path, "/users/@me/channels"):
					dms = append(dms, "dm")
					return discordResponse(r, 200, `{"id":"dm2001"}`), nil
				case strings.HasSuffix(path, "/channels/dm2001/messages"):
					return discordResponse(r, 200, `{"id":"m1","channel_id":"dm2001","content":"x"}`), nil
				}
				t.Fatalf("unexpected Discord request %s %s", r.Method, path)
				return nil, nil
			})
			b := &Bot{castaway: client, session: session, targetServerIDs: []string{"101"}, adminContactID: "235246238382030849", log: slog.New(slog.NewTextHandler(io.Discard, nil))}

			err = b.deliverDiscussionThread(t.Context())
			if (err != nil) != (tc.wantFailure != "") {
				t.Fatalf("err %v, want failure %q", err, tc.wantFailure)
			}
			wantLines := 0
			if tc.wantLine {
				wantLines = 1
			}
			if threads != tc.wantThreads || deletes != tc.wantThreads || lines != wantLines {
				t.Fatalf("threads %d deletes %d lines %d; want threads %d lines %d", threads, deletes, lines, tc.wantThreads, wantLines)
			}
			if len(dms) != map[bool]int{true: 1, false: 0}[tc.wantDM] {
				t.Fatalf("admin DMs %d, want DM %v", len(dms), tc.wantDM)
			}
			if tc.wantFailure == "" {
				if finish.Failure != "" || finish.ThreadID != "9001" || finish.LineMessageID != "9002" {
					t.Fatalf("success recorded %+v", finish)
				}
			} else if !strings.Contains(finish.Failure, tc.wantFailure) {
				t.Fatalf("recorded failure %q want %q", finish.Failure, tc.wantFailure)
			}
		})
	}
}

func mustWrite(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Error(err)
	}
}
