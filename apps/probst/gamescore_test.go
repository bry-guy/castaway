package main

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Plural names take plural verbs: no "Riley and Keeling is running" or "Keeling, at 13, is playing".
func TestLeaderLinesAgreeWithPlural(t *testing.T) {
	set := mustTemplates(t, "../../seasons/51-templates")
	singular := regexp.MustCompile(`Keeling(,[^,]*,)?\s+(is|sits|leads|holds|rules|finishes)\b`)
	for week := 1; week <= 13; week++ {
		line, err := set.line("leader", week, lineData{Names: "Riley and Keeling", N: 13, Plural: true})
		if err != nil {
			t.Fatal(err)
		}
		if m := singular.FindString(line); m != "" {
			t.Errorf("week %d: singular verb after plural names: %q", week, line)
		}
	}
	line, err := set.line("gainer", 1, lineData{Names: "Riley and Keeling", N: 2, Plural: true})
	if err != nil || strings.Contains(line, " is on a heater") {
		t.Errorf("gainer: %q %v", line, err)
	}
}

// Each game section renders from the same gameScore path the scores post uses, with results as the API returns them.
func TestGameSectionsRender(t *testing.T) {
	set := mustTemplates(t, "../../seasons/51-templates")
	cases := map[string]struct {
		results string
		want    []string
	}{
		"spell_it_out": {`{"players":[
			{"name":"Ann","tribe_emoji":"🟣","place":1,"points":5,"hint_points":2,"clean_points":2},
			{"name":"Bo","tribe_emoji":"🟡","place":2,"points":3,"hint_points":1},
			{"name":"Cy","tribe_emoji":"🟣","place":0}]}`,
			[]string{"🔠 **Island Scramble**:", "🟣 Ann (+5) 💡 +2 🎯 +2\n🟡 Bo (+3) 💡 +1"}},
		"inside_track": {`{"players":[
			{"name":"Ann","tribe_emoji":"🟣","points":3,"score":4,"cups":2,"balls_left":2},
			{"name":"Bo","tribe_emoji":"🟡","points":1,"score":1,"cups":1}]}`,
			[]string{"🎯 **Inside Track**:", "🟣 Ann (+3) · 4 (2 cups + 2 balls left)\n🟡 Bo (+1) · 1 (1 cup)"}},
		"free_the_block": {`{"players":[
			{"name":"Ann","tribe_emoji":"🟣","points":3,"moves":81,"solved":true},
			{"name":"Bo","tribe_emoji":"🟡","points":1,"moves":83,"solved":true},
			{"name":"Cy","tribe_emoji":"🟣","points":0,"moves":40}]}`,
			[]string{"🧱 **Free the Block**:", "🟣 Ann (+3) · 81 moves\n🟡 Bo (+1) · 83 moves"}},
	}
	for game, c := range cases {
		var f seasonFile
		if err := yaml.Unmarshal([]byte("weeks:\n  3:\n    game: {type: "+game+"}\n"), &f); err != nil {
			t.Fatal(err)
		}
		fake := func(_ context.Context, _, _ string, _, out any) error { return json.Unmarshal([]byte(c.results), out) }
		text, err := gameScore(context.Background(), fake, "/instances/x", set, f, 3)
		if err != nil {
			t.Fatalf("%s: %v", game, err)
		}
		if strings.Contains(text, "<no value>") {
			t.Errorf("%s has <no value>: %q", game, text)
		}
		for _, want := range c.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s: missing %q in:\n%s", game, want, text)
			}
		}
	}
}
