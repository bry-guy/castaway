package main

import (
	"strings"
	"testing"
)

// The live season templates parse and render with sample data (templates only; no season dates).
func TestLiveTemplatesRender(t *testing.T) {
	set := mustTemplates(t, "../../seasons/51-templates")
	p := scoresPost{Season: 51, Week: 3, Intro: "Hi", Leader: "L", Gainer: "G", Slider: "S", Boots: "B",
		All: []rankLine{{"<@1>", 9, 1}, {"<@2>", 8, 2}, {"<@3>", 7, 3}, {"Dee", 5, 4}, {"<@4>", 1, 5}}, Site: "https://x", NextGame: "NEXT", GameScore: "GAME"}
	text, err := renderScoresPost(set, p)
	if err != nil || !strings.Contains(text, "GAME") || !strings.Contains(text, "NEXT") || !strings.Contains(text, "3. <@3>: 7") {
		t.Fatalf("scores.md: %v\n%s", err, text)
	}
	players := []gameRow{{Name: "Ann", TribeEmoji: "🟣", Points: 3, Presses: 20, First: true, Place: 1, Guesses: 3, Solved: true}, {Name: "Bo", Points: -1, Presses: 2, Last: true}}
	tribes := []tribeRow{{Name: "Savu", Emoji: "🟣", Value: 3.7}, {Name: "Toka", Emoji: "🟡", Value: 4}}
	for game := range gameNames {
		text, err := set.render("games/"+game+".md", map[string]any{"Name": gameNames[game], "Week": 3, "Players": players, "Tribes": tribes, "Rules": buttonRules(players)})
		if err != nil || !strings.Contains(text, "Ann") {
			t.Errorf("games/%s.md: %v\n%s", game, err, text)
		}
	}
	for _, name := range []string{"reminder.md", "next-game.md"} {
		if _, err := set.render(name, map[string]any{"Week": 3, "Type": "castawordle", "Name": "Castawordle", "Site": "https://x", "Closes": "7:59pm", "Opens": "8pm", "Open": ""}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	sample := map[string]any{"Episode": 3, "Week": 3, "Season": 51, "Why": "w", "Holds": []string{"h"}, "Review": []string{"r"}, "Revision": "abc", "Results": []string{"boot: Ori"},
		"Boots": []map[string]any{{"Name": "Rob", "Position": 19, "Result": "Quit"}}, "Challenges": []map[string]any{{"Tribes": "Savu", "Kind": "immunity", "Points": "+2"}},
		"Champions": []map[string]any{}, "Error": "e", "At": "a", "Waiting": "w", "Until": "u", "Game": "g", "Problem": "p", "Opens": "o", "Issues": []string{"i"}, "Kind": "swap", "Tribes": []swapTribe{}, "Awards": []tribeAward{}}
	for name := range set {
		if strings.HasPrefix(name, "messages/") {
			if text, err := set.render(name, sample); err != nil || text == "" {
				t.Errorf("%s: %v", name, err)
			}
		}
		if !strings.HasPrefix(name, "lines/") {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(name, "lines/"), ".txt")
		seen := map[string]bool{}
		for week := 1; week <= 13; week++ {
			line, err := set.line(key, week, lineData{Names: "X", N: 2})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if seen[line] && strings.Count(strings.TrimSpace(set[name]), "\n") > 0 {
				t.Errorf("%s repeats by week %d", name, week)
			}
			seen[line] = true
		}
	}
}
