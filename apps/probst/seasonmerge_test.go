package main

import (
	"strings"
	"testing"
	"time"
)

// The window defaults to merge episode → next episode; scoring.champion.opens/locks override it.
func TestChampionRoundBody(t *testing.T) {
	et := eastern()
	airs := []time.Time{time.Date(2026, 11, 4, 20, 0, 0, 0, et), time.Date(2026, 11, 11, 20, 0, 0, 0, et), time.Date(2026, 11, 18, 20, 0, 0, 0, et)}
	var f seasonFile
	f.Scoring.MergeEpisode = 1
	body, _, err := championRoundBody(f, airs)
	if err != nil || body["opens_at"] != "2026-11-05T01:00:00Z" || body["locks_at"] != "2026-11-12T01:00:00Z" {
		t.Fatalf("default: %v %v", body, err)
	}
	f.Scoring.Champion = map[string]any{"opens": "2026-11-12 01:00", "locks": "+14d 20:00"}
	body, line, err := championRoundBody(f, airs)
	if err != nil || body["opens_at"] != "2026-11-12T06:00:00Z" || body["locks_at"] != "2026-11-19T01:00:00Z" || !strings.Contains(line, "Wed Nov 18 8:00pm") {
		t.Fatalf("override: %v %q %v", body, line, err)
	}
	f.Scoring.Champion = map[string]any{"locks": "2026-11-01 20:00"}
	if _, _, err := championRoundBody(f, airs); err == nil {
		t.Fatal("a lock before the open should fail")
	}
}
