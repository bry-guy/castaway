package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Sync refuses while live episodes disagree with the file, saves only from the revision it read, and the
// scheduled job then runs from the synced copy alone.
func TestSeasonSync(t *testing.T) {
	const inst = "605699c6-1f21-4741-8e0f-4864d5e62018"
	var stored *syncedSeason
	var puts []map[string]string
	shiftEp := 0 // when set, that live episode airs an hour off the file
	call := func(_ context.Context, method, path string, body, out any) error {
		write := func(v any) error { b, _ := json.Marshal(v); return json.Unmarshal(b, out) }
		switch {
		case path == "/instances/"+inst && method == "GET":
			var eps []episode
			at := time.Date(2026, 9, 23, 20, 0, 0, 0, eastern())
			for n := 1; n <= 13; n++ {
				air := at.AddDate(0, 0, 7*(n-1))
				if n == shiftEp {
					air = air.Add(time.Hour)
				}
				eps = append(eps, episode{Number: n, AirsAt: air})
			}
			return write(map[string]any{"episodes": eps})
		case strings.HasSuffix(path, "/scheduled-games"):
			return write(map[string]any{"games": []scheduledGame{}})
		case strings.HasSuffix(path, "/announcements"):
			return write(map[string]any{"announcements": []announcement{{RequestKey: "s51-week-3-scores", Status: "draft"}}})
		case strings.HasSuffix(path, "/season-config") && method == "GET":
			if stored == nil {
				return fmt.Errorf("HTTP 404: none")
			}
			return write(stored)
		case strings.HasSuffix(path, "/season-config") && method == "PUT":
			req := body.(map[string]string)
			puts = append(puts, req)
			stored = &syncedSeason{SeasonYAML: req["season_yaml"], ScoresTemplate: req["scores_template"], Revision: fmt.Sprintf("r%d", len(puts))}
			return write(map[string]string{"revision": stored.Revision})
		}
		return fmt.Errorf("unexpected %s %s", method, path)
	}
	run := func(yes bool, args ...string) (string, error) {
		instance, guild := "", ""
		cmd := newSeasonCommand(call, &instance, &guild, &yes)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		cmd.SetContext(context.Background())
		err := cmd.Execute()
		return out.String(), err
	}
	file := "testdata/season-brainland.yaml"

	shiftEp = 5
	if _, err := run(true, "sync", file); err == nil || !strings.Contains(err.Error(), "Episode 5") {
		t.Fatalf("sync with a mismatched live episode: %v", err)
	}
	shiftEp = 0
	if out, err := run(false, "sync", file); err != nil || len(puts) != 0 || !strings.Contains(out, "Dry run") {
		t.Fatalf("dry run: %v %q %d", err, out, len(puts))
	}
	if _, err := run(true, "sync", file); err != nil || len(puts) != 1 || puts[0]["base_revision"] != "" {
		t.Fatalf("first sync: %v %v", err, puts)
	}
	raw, _ := os.ReadFile(file)
	tmpl := mustTemplates(t, "testdata/templates").bundle()
	if puts[0]["season_yaml"] != string(raw) || puts[0]["scores_template"] != string(tmpl) {
		t.Fatal("sync didn't save the file and its scores template")
	}
	if out, err := run(true, "sync", file); err != nil || len(puts) != 1 || !strings.Contains(out, "Already synced") {
		t.Fatalf("unchanged re-sync: %v %q", err, out)
	}
	stored.SeasonYAML += "\n# edited\n" // someone synced a different version
	if _, err := run(true, "sync", file); err != nil || len(puts) != 2 || puts[1]["base_revision"] != "r1" {
		t.Fatalf("re-sync must name the revision it replaces: %v %v", err, puts)
	}

	// The job reads the synced copy, not a file.
	instance, guild, yes := inst, "", false
	cmd := newSeasonCommand(call, &instance, &guild, &yes)
	cmd.SetContext(context.Background())
	r, err := newSyncedReconciler(cmd, call, inst)
	if err != nil || r.f.Season != 51 || r.tmpl.bundle() != tmpl || len(r.airs) != 13 {
		t.Fatalf("synced reconciler: %v", err)
	}
	if _, err := newSyncedReconciler(cmd, call, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Fatal("ran another instance's synced config")
	}
}

// A week whose game type is set but has no live game shows up as a difference (it stays unplayable otherwise).
func TestSeasonDiffMissingGame(t *testing.T) {
	f, err := loadSeasonFile("testdata/season-brainland.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, airs, err := expandSeason(f, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call := func(_ context.Context, method, path string, body, out any) error {
		write := func(v any) error { b, _ := json.Marshal(v); return json.Unmarshal(b, out) }
		switch {
		case strings.HasSuffix(path, "/scheduled-games"):
			return write(map[string]any{"games": []scheduledGame{{Type: "castawordle", Episode: 2}}})
		case strings.HasSuffix(path, "/announcements"):
			return write(map[string]any{"announcements": []announcement{}})
		default:
			var eps []episode
			for i, a := range airs {
				eps = append(eps, episode{Number: i + 1, AirsAt: a})
			}
			return write(map[string]any{"episodes": eps})
		}
	}
	diffs, err := seasonDiff(context.Background(), call, f, airs)
	got := strings.Join(diffs, "\n")
	if err != nil || strings.Contains(got, "Week 2 ") || !strings.Contains(got, "Week 3 Press the Button: not created live") {
		t.Fatalf("%v\n%s", err, got)
	}
}
