package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type discussionCall struct {
	puts []map[string]any
	keys []string
}

func (c *discussionCall) call(_ context.Context, method, path string, body, out any) error {
	if method != "PUT" || !strings.Contains(path, "/discussion-threads/") {
		return nil
	}
	c.keys = append(c.keys, path[strings.LastIndex(path, "/")+1:])
	raw, _ := json.Marshal(body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	c.puts = append(c.puts, m)
	return json.Unmarshal([]byte(`{"action":"created"}`), out)
}

// Episode 6 airs at airs[5]; the thread is queued from a day before through three hours after air.
func TestDiscussionQueueWindow(t *testing.T) {
	airs := []time.Time{}
	for i := 0; i < 8; i++ {
		airs = append(airs, time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 7*i).Add(24*time.Hour))
	}
	air := airs[5]
	for _, tc := range []struct {
		name    string
		feature bool // weekly.discussion_thread
		enabled bool // automation.enabled: the feature doesn't depend on it
		now     time.Time
		want    int // PUTs for episode 6 (other episodes are outside their own windows here)
	}{
		{"feature off queues nothing", false, true, air, 0},
		{"automation off still queues", true, false, air, 1},
		{"a day early queues it", true, true, air.Add(-24 * time.Hour), 1},
		{"at air time", true, true, air, 1},
		{"three hours late still queues", true, true, air.Add(3 * time.Hour), 1},
		{"after the window never opens an old thread", true, true, air.Add(4 * time.Hour), 0},
		{"too early to queue", true, true, air.Add(-25 * time.Hour), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c discussionCall
			f := seasonFile{Season: 51, Channel: "7002"}
			f.Automation.Enabled = tc.enabled
			f.Automation.Guild = "7001"
			f.Weekly.DiscussionThread = tc.feature
			r := &reconciler{ctx: context.Background(), call: c.call, f: f, airs: airs, now: tc.now, path: "/instances/x", out: &bytes.Buffer{}}
			if err := r.queueDiscussions(); err != nil {
				t.Fatal(err)
			}
			if len(c.puts) != tc.want {
				t.Fatalf("queued %d, want %d: %v", len(c.puts), tc.want, c.puts)
			}
			if tc.want == 1 {
				if c.keys[0] != "s51-ep6-discussion" || c.puts[0]["name"] != "Survivor S51 E6 Discussion" || c.puts[0]["channel_id"] != "7002" || c.puts[0]["guild_id"] != "7001" || c.puts[0]["due_at"] != air.UTC().Format(time.RFC3339) {
					t.Fatalf("bad queue request %v %v", c.keys, c.puts[0])
				}
			}
		})
	}
}

func TestDiscussionOpenCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yes     bool
		args    []string
		wantPut bool
		wantErr bool
	}{
		{"dry run by default", false, []string{"open", "--season", "51", "--episode", "6", "--guild", "7001", "--channel", "7002"}, false, false},
		{"--yes queues it now", true, []string{"open", "--season", "51", "--episode", "6", "--guild", "7001", "--channel", "7002"}, true, false},
		{"missing flags refuse", true, []string{"open", "--season", "51", "--guild", "7001", "--channel", "7002"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c discussionCall
			instance := "instance-uuid"
			yes := tc.yes
			cmd := newDiscussionCommand(c.call, &instance, &yes)
			cmd.SetArgs(tc.args)
			out := &bytes.Buffer{}
			cmd.SetOut(out)
			cmd.SetErr(out)
			err := cmd.Execute()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err %v", err)
			}
			if (len(c.puts) == 1) != tc.wantPut {
				t.Fatalf("puts %v", c.puts)
			}
			if tc.wantPut && c.puts[0]["name"] != "Survivor S51 E6 Discussion" {
				t.Fatalf("name %v", c.puts[0])
			}
		})
	}
}
