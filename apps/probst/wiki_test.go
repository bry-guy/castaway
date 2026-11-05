package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func wikiFixture(t *testing.T, name string) wikiPage {
	t.Helper()
	b, err := os.ReadFile("testdata/wiki/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return wikiPage{Title: name, Text: string(b), RevID: 7}
}

// The real Season 51 wiki pages (Nov 2026): episode 6 is a tribe immunity, episode 7 the merge.
func TestPlanWikiEpisode(t *testing.T) {
	names := []string{"Ori Jean-Charles", "Brady Booker", "Carter Krull", "Devin Way", "Eric Macksoud", "Jelly Loblack", "Danny Kilby",
		"Lewis Kelly", "Maggie Nestor", "Mike Pinsky", "Thien An Nguyen"}
	var rows []contestant
	for i := 0; i < 21; i++ {
		name := fmt.Sprintf("Filler %c", 'A'+i)
		if i < len(names) {
			name = names[i]
		}
		rows = append(rows, contestant{ID: fmt.Sprint(i), Name: name})
	}
	roster := newRoster(rows)
	guide := wikiFixture(t, "guide")
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)

	p := planWikiEpisode(51, 6, wikiFixture(t, "ep6"), guide, roster, importRules{TribeFrom: 3}, now)
	if got := p.describe(); p.Status != "ready" || len(p.Holds)+len(p.Review) != 0 || p.Revision != "wiki:7" ||
		!strings.Contains(got, "boot: Ori Jean-Charles at place 14 (7th voted out)") || len(p.Challenges) != 1 ||
		p.Challenges[0].Kind != "immunity" || strings.Join(p.Challenges[0].Tribes, ",") != "Dina,Toka" || p.Challenges[0].LegacyKeys[0] != "ep6-immunity" {
		t.Fatalf("ep6:\n%s\n%+v", got, p.Challenges)
	}

	p = planWikiEpisode(51, 7, wikiFixture(t, "ep7"), guide, roster, importRules{TribeFrom: 3, MergeEpisode: 6}, now)
	got := p.describe()
	if p.Status != "ready" || len(p.Holds) != 0 || !strings.Contains(got, "Brady Booker at place 13") || len(p.Champions) != 2 ||
		len(p.Champions[0].Winners) != 10 || p.Champions[1].Kind != "immunity" || p.Champions[1].Winners[0].Name != "Mike Pinsky" {
		t.Fatalf("ep7:\n%s", got)
	}

	// An unfinished page (episode 8 isn't up), and a fresh edit, wait.
	if p := planWikiEpisode(51, 8, wikiPage{}, guide, roster, importRules{}, now); p.Status != "incomplete" {
		t.Fatalf("ep8: %+v", p)
	}
	fresh := wikiFixture(t, "ep6")
	fresh.Edited = now.Add(-10 * time.Minute)
	if p := planWikiEpisode(51, 6, fresh, guide, roster, importRules{TribeFrom: 3}, now); p.Status != "incomplete" || !strings.Contains(p.Why, "last hour") {
		t.Fatalf("fresh edit: %+v", p)
	}
}
