package discord

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bry-guy/srvivor/apps/castaway-discord-bot/internal/castaway"
	"github.com/bwmarrin/discordgo"
)

const publishRequestID = "7d1f0c52-8a3e-4b6e-9f10-2c4d5e6f7a81"

type publishSent struct {
	content string
	parse   []string
}

// Admin DM publish flow: echo the exact text, list channels, then a reply names the server(s). The server call
// is faked, since server matching is covered by its own integration test.
func TestAdminPublishFlow(t *testing.T) {
	const text = "Hi all, <@456> is not a ping"
	both := []castaway.PublishTarget{{Name: "Podracing", GuildID: "101", ChannelID: "7002"}, {Name: "BrainLand", GuildID: "101", ChannelID: "7004"}}
	for _, tc := range []struct {
		name       string
		targets    []castaway.PublishTarget
		reply      string
		replyJSON  string
		wantReply  string   // reply text the bot forwards to the server ("" = no reply forwarded)
		wantAnswer string   // substring of the bot's answer to the reply
		wantSent   []string // Discord sends after the admin's DM (echo, prompt, then the answer)
	}{
		{
			name:       "single server",
			targets:    both[:1],
			reply:      "podracing",
			replyJSON:  `{"status":"posted","posted":[{"name":"Podracing","guild_id":"101","channel_id":"7002"}],"targets":[]}`,
			wantReply:  "podracing",
			wantAnswer: "Queued for Podracing.",
		},
		{
			name:       "multiple servers",
			targets:    both,
			reply:      "Podracing/BrainLand",
			replyJSON:  `{"status":"posted","posted":[{"name":"Podracing","guild_id":"101","channel_id":"7002"},{"name":"BrainLand","guild_id":"101","channel_id":"7004"}],"targets":[]}`,
			wantReply:  "Podracing/BrainLand",
			wantAnswer: "Queued for Podracing and BrainLand.",
		},
		{
			name:       "unknown name keeps the options",
			targets:    both,
			reply:      "Survivor",
			replyJSON:  `{"status":"unknown","unknown":["Survivor"],"targets":[{"name":"Podracing","guild_id":"101","channel_id":"7002"},{"name":"BrainLand","guild_id":"101","channel_id":"7004"}]}`,
			wantReply:  "Survivor",
			wantAnswer: "No server named Survivor. Reply with one of these",
		},
		{
			name:       "cancel",
			targets:    both,
			reply:      "no",
			replyJSON:  `{"status":"cancelled","targets":[]}`,
			wantReply:  "no",
			wantAnswer: "Cancelled. Nothing was posted.",
		},
		{
			name:    "non-admin gets nothing",
			targets: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var forwarded string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Reply string `json:"reply"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				switch {
				case r.URL.Path == "/admin-dms/publish-requests":
					id := any(nil)
					if len(tc.targets) > 0 {
						id = publishRequestID
					}
					if err := json.NewEncoder(w).Encode(map[string]any{"request_id": id, "targets": tc.targets}); err != nil {
						t.Error(err)
					}
				case r.URL.Path == "/admin-dms/publish-requests/"+publishRequestID+"/reply":
					forwarded = body.Reply
					if _, err := io.WriteString(w, tc.replyJSON); err != nil {
						t.Error(err)
					}
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
			var sent []publishSent
			stored := map[string]*discordgo.Message{}
			next := 1
			session.Client.Transport = announcementRoundTrip(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/messages") && r.Method == http.MethodPost {
					var body struct {
						Content         string `json:"content"`
						AllowedMentions struct {
							Parse []string `json:"parse"`
						} `json:"allowed_mentions"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					sent = append(sent, publishSent{content: body.Content, parse: body.AllowedMentions.Parse})
					id := fmt.Sprint(900 + next)
					next++
					msg := &discordgo.Message{ID: id, ChannelID: "dm1", Content: body.Content, Author: &discordgo.User{ID: "999", Bot: true}}
					stored[id] = msg
					out, err := json.Marshal(msg)
					if err != nil {
						t.Error(err)
					}
					return discordResponse(r, 200, string(out)), nil
				}
				if i := strings.Index(r.URL.Path, "/channels/dm1/messages/"); i >= 0 && r.Method == http.MethodGet {
					out, err := json.Marshal(stored[r.URL.Path[i+len("/channels/dm1/messages/"):]])
					if err != nil {
						t.Error(err)
					}
					return discordResponse(r, 200, string(out)), nil
				}
				t.Fatalf("unexpected Discord request %s %s", r.Method, r.URL.Path)
				return nil, nil
			})
			b := &Bot{castaway: client, session: session, targetServerIDs: []string{"101"}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}

			b.onMessageCreate(session, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "1", ChannelID: "dm1", Content: text, Author: &discordgo.User{ID: "2001"}}})
			if tc.targets == nil {
				if len(sent) != 0 || forwarded != "" {
					t.Fatalf("non-admin DM answered: %+v", sent)
				}
				return
			}
			if len(sent) != 2 || sent[0].content != text || len(sent[0].parse) != 0 {
				t.Fatalf("echo must be the exact text with no pings: %+v", sent)
			}
			prompt := sent[1].content
			for _, want := range []string{"<#7002>", "publish `" + publishRequestID + "`"} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("prompt missing %q: %s", want, prompt)
				}
			}
			if len(tc.targets) == 2 && !strings.Contains(prompt, "<#7004>") {
				t.Fatalf("prompt misses a target channel: %s", prompt)
			}

			promptID := fmt.Sprint(900 + 2)
			b.onMessageCreate(session, &discordgo.MessageCreate{Message: &discordgo.Message{
				ID: "3", ChannelID: "dm1", Content: tc.reply, Author: &discordgo.User{ID: "2001"},
				MessageReference: &discordgo.MessageReference{MessageID: promptID, ChannelID: "dm1"},
			}})
			if forwarded != tc.wantReply {
				t.Fatalf("forwarded reply %q want %q", forwarded, tc.wantReply)
			}
			if len(sent) != 3 || !strings.Contains(sent[2].content, tc.wantAnswer) {
				t.Fatalf("answer %+v want substring %q", sent, tc.wantAnswer)
			}
		})
	}
}
