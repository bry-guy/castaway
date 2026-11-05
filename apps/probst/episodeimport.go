package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Safe, unattended episode import from survivoR. Everything comes from one pinned upstream commit; an
// episode is imported only when survivoR lists it and has its boot and every listed challenge's results.
// Anything we can't score with confidence becomes a hold for an admin instead of a guess, and the server
// applies the rest in one transaction (or not at all).

var votedOut = regexp.MustCompile(`^\d+(st|nd|rd|th) voted out$`)

// survivorRevision is the latest survivoR commit touching the data, so every table read comes from the
// same snapshot. With PROBST_SURVIVOR_URL set (tests, mirrors) the revision is that URL.
func survivorRevision(ctx context.Context) (base, revision string, err error) {
	if override := os.Getenv("PROBST_SURVIVOR_URL"); override != "" {
		return override, override, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/doehm/survivoR/commits?path=dev/json&per_page=1", nil)
	if err != nil {
		return "", "", err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("survivoR revision: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&commits); err != nil || resp.StatusCode != 200 || len(commits) == 0 || len(commits[0].SHA) != 40 {
		return "", "", fmt.Errorf("survivoR revision: HTTP %d %v", resp.StatusCode, err)
	}
	return "https://raw.githubusercontent.com/doehm/survivoR/" + commits[0].SHA + "/dev/json", commits[0].SHA, nil
}

// episodePlan is what an import would do, and what it won't do without an admin.
type episodePlan struct {
	Episode    int
	Revision   string
	Status     string // absent, incomplete, ready
	Why        string // for absent/incomplete
	Boots      []planBoot
	Challenges []planChallenge
	Champions  []planChampion // after the merge
	Holds      []string       // can't be imported: needs fixing by hand
	Review     []string       // importable, but an admin approves it first (by replying yes)
}

type planBoot struct {
	Name, ContestantID, Result string
	Position                   int
}

// planChampion is a post-merge challenge won by castaways; their locked pickers score.
type planChampion struct {
	Key     string
	Kind    string
	Winners []planBoot // Name and ContestantID
	Label   string
}

type planChallenge struct {
	Key        string
	LegacyKeys []string
	Kind       string
	Tribes     []string
	Label      string
}

type importRules struct {
	TribeFrom    int // first episode whose tribe challenges score; 0 = never
	MergeEpisode int // 0 = not set; tribe scoring stops here
}

func num(v any) int    { f, _ := v.(float64); return int(f) }
func str(v any) string { s, _ := v.(string); return s }

// planEpisode classifies one episode's survivoR data against the roster.
func planEpisode(data survivorEpisode, revision string, roster []*contestant, rules importRules) episodePlan {
	p := episodePlan{Episode: data.Episode, Revision: revision, Status: "ready"}
	listed := false
	for _, e := range data.Tables["episodes"] {
		listed = listed || num(e["episode"]) == data.Episode
	}
	if !listed {
		p.Status, p.Why = "absent", "survivoR doesn't list the episode yet"
		return p
	}

	// Boots: castaways' final place, cross-checked by castaway_id against boot_order.
	places := map[string]map[string]any{}
	for _, c := range data.Tables["castaways"] {
		places[str(c["castaway_id"])] = c
	}
	boots := data.Tables["boot_order"]
	if len(boots) == 0 {
		p.Holds = append(p.Holds, "no boot recorded for this episode (no elimination, or survivoR is behind)")
	}
	if len(boots) > 1 {
		p.Review = append(p.Review, fmt.Sprintf("%d people left this episode", len(boots)))
	}
	seen, seenPlace := map[string]bool{}, map[int]bool{}
	for _, b := range boots {
		c := places[str(b["castaway_id"])]
		if id, place := str(b["castaway_id"]), num(c["place"]); seen[id] || (place > 0 && seenPlace[place]) {
			p.Holds = append(p.Holds, "survivoR lists the same exit twice")
			continue
		} else {
			seen[id], seenPlace[place] = true, true
		}
		result, name, place := str(b["result"]), str(c["full_name"]), num(c["place"])
		match, method, _ := matchName(normalize(name), roster)
		switch {
		case c == nil || place <= 0:
			p.Status, p.Why = "incomplete", "boot "+str(b["castaway"])+" has no final place yet"
			return p
		case match == nil || method != "exact":
			p.Holds = append(p.Holds, fmt.Sprintf("boot %q doesn't exactly match a contestant", name))
			continue
		case !votedOut.MatchString(result):
			p.Review = append(p.Review, fmt.Sprintf("%s left by %q, not a vote", match.Name, result))
		}
		p.Boots = append(p.Boots, planBoot{Name: match.Name, ContestantID: match.ID, Position: place, Result: result})
	}

	// Challenges: every challenge survivoR describes must have results.
	described := map[int]string{}
	for _, d := range data.Tables["challenge_description"] {
		described[num(d["challenge_id"])] = str(d["name"])
	}
	type result struct {
		kind, outcome string
		won, all      map[string]bool
		winners       []string // castaway_ids of rows with won=1
	}
	results := map[int]*result{}
	for _, r := range data.Tables["challenge_results"] {
		id := num(r["challenge_id"])
		res := results[id]
		if res == nil {
			res = &result{kind: str(r["challenge_type"]), outcome: str(r["outcome_type"]), won: map[string]bool{}, all: map[string]bool{}}
			results[id] = res
		}
		tribe := str(r["tribe"])
		res.all[tribe] = true
		if num(r["won"]) == 1 {
			res.won[tribe] = true
			if id := str(r["castaway_id"]); id != "" && !contains(res.winners, id) {
				res.winners = append(res.winners, id)
			}
		}
	}
	if len(described) == 0 {
		p.Status, p.Why = "incomplete", "survivoR has no challenges for the episode yet"
		return p
	}
	ids := make([]int, 0, len(described))
	for id := range described {
		ids = append(ids, id)
		if results[id] == nil {
			p.Status, p.Why = "incomplete", fmt.Sprintf("challenge %d %q has no results yet", id, described[id])
			return p
		}
	}
	sort.Ints(ids)
	tribeScoring := rules.TribeFrom > 0 && data.Episode >= rules.TribeFrom && (rules.MergeEpisode == 0 || data.Episode < rules.MergeEpisode)
	if rules.MergeEpisode > 0 && data.Episode == rules.MergeEpisode {
		p.Holds = append(p.Holds, "this is the merge episode: Tribe scoring has ended and Champions aren't locked yet, so its challenges don't score; confirm by hand")
		return p
	}
	if rules.MergeEpisode > 0 && data.Episode > rules.MergeEpisode {
		for _, id := range ids {
			r := results[id]
			label := fmt.Sprintf("challenge %d %q (%s %s)", id, described[id], r.outcome, r.kind)
			kind := strings.ToLower(r.kind)
			var kinds []string
			for _, k := range []string{"immunity", "reward"} {
				if strings.Contains(kind, k) {
					kinds = append(kinds, k)
				}
			}
			if len(kinds) == 0 {
				continue
			}
			var winners []planBoot
			for _, cid := range r.winners {
				name := str(places[cid]["full_name"])
				match, method, _ := matchName(normalize(name), roster)
				if match == nil || method != "exact" {
					p.Holds = append(p.Holds, fmt.Sprintf("%s: winner %q doesn't exactly match a contestant", label, name))
					winners = nil
					break
				}
				winners = append(winners, planBoot{Name: match.Name, ContestantID: match.ID})
			}
			if len(r.winners) == 0 {
				p.Holds = append(p.Holds, label+": no winner listed, decide by hand")
			}
			if len(winners) == 0 {
				continue
			}
			for _, k := range kinds {
				p.Champions = append(p.Champions, planChampion{Key: fmt.Sprintf("survivor-ep%d-c%d-%s", data.Episode, id, k), Kind: k, Winners: winners, Label: label})
			}
		}
		return p
	}
	if !tribeScoring {
		return p // challenges don't score this episode
	}
	perKind := map[string]int{}
	for _, id := range ids {
		r := results[id]
		kind := strings.ToLower(r.kind)
		for _, k := range []string{"immunity", "reward"} {
			if strings.Contains(kind, k) {
				perKind[k]++
			}
		}
	}
	for _, id := range ids {
		r := results[id]
		label := fmt.Sprintf("challenge %d %q (%s %s)", id, described[id], r.outcome, r.kind)
		kind := strings.ToLower(r.kind)
		var kinds []string
		for _, k := range []string{"immunity", "reward"} {
			if strings.Contains(kind, k) {
				kinds = append(kinds, k)
			}
		}
		if len(kinds) == 0 {
			continue // e.g. a duel: nothing to score
		}
		var tribes []string
		for t := range r.won {
			tribes = append(tribes, t)
		}
		sort.Strings(tribes)
		switch {
		case r.outcome != "Tribal":
			p.Holds = append(p.Holds, label+": not a plain tribe win, decide by hand")
			continue
		case len(tribes) == 0 || len(tribes) == len(r.all) || contains(tribes, ""):
			p.Holds = append(p.Holds, label+": no clear winning tribe, decide by hand")
			continue
		}
		for _, k := range kinds {
			ch := planChallenge{Key: fmt.Sprintf("survivor-ep%d-c%d-%s", data.Episode, id, k), Kind: k, Tribes: tribes, Label: label}
			if perKind[k] == 1 { // the hand-entry key `probst challenge` uses; only unambiguous with one per kind
				ch.LegacyKeys = []string{fmt.Sprintf("ep%d-%s", data.Episode, k)}
			}
			p.Challenges = append(p.Challenges, ch)
		}
	}
	return p
}

// importBody is the server request for a plan (held items left out).
func (p episodePlan) importBody(airsAt time.Time, manual bool) map[string]any {
	boots := []map[string]any{}
	for _, b := range p.Boots {
		boots = append(boots, map[string]any{"position": b.Position, "contestant_id": b.ContestantID})
	}
	champions := []map[string]any{}
	for _, ch := range p.Champions {
		ids := []string{}
		for _, w := range ch.Winners {
			ids = append(ids, w.ContestantID)
		}
		champions = append(champions, map[string]any{"key": ch.Key, "kind": ch.Kind, "winners": ids,
			"effective_at": airsAt.Add(challengeOffset).UTC().Format(time.RFC3339)})
	}
	challenges := []map[string]any{}
	for _, ch := range p.Challenges {
		challenges = append(challenges, map[string]any{"key": ch.Key, "legacy_keys": ch.LegacyKeys, "kind": ch.Kind, "winning_tribes": ch.Tribes,
			"effective_at": airsAt.Add(challengeOffset).UTC().Format(time.RFC3339)})
	}
	source := "survivor"
	switch {
	case manual:
		source = "manual"
	case strings.HasPrefix(p.Revision, "wiki:"):
		source = "wiki"
	}
	return map[string]any{"episode_number": p.Episode, "source": source, "source_revision": p.Revision, "boots": boots, "challenges": challenges, "champion_challenges": champions}
}

func (p episodePlan) describe() string {
	var b strings.Builder
	from := "survivoR " + shortRev(p.Revision)
	if rev, ok := strings.CutPrefix(p.Revision, "wiki:"); ok {
		from = "Survivor wiki rev " + rev
	}
	fmt.Fprintf(&b, "Episode %d (%s): %s", p.Episode, from, p.Status)
	if p.Why != "" {
		fmt.Fprintf(&b, " — %s", p.Why)
	}
	for _, x := range p.Boots {
		fmt.Fprintf(&b, "\n  boot: %s at place %d (%s)", x.Name, x.Position, x.Result)
	}
	for _, ch := range p.Challenges {
		fmt.Fprintf(&b, "\n  %s: %s won %s", ch.Label, strings.Join(ch.Tribes, ", "), ch.Kind)
	}
	for _, ch := range p.Champions {
		var names []string
		for _, w := range ch.Winners {
			names = append(names, w.Name)
		}
		fmt.Fprintf(&b, "\n  %s: %s won %s (their Champion pickers score)", ch.Label, strings.Join(names, ", "), ch.Kind)
	}
	for _, r := range p.Review {
		fmt.Fprintf(&b, "\n  REVIEW: %s", r)
	}
	for _, h := range p.Holds {
		fmt.Fprintf(&b, "\n  HOLD: %s", h)
	}
	return b.String()
}

func shortRev(r string) string {
	if len(r) == 40 {
		return r[:7]
	}
	return r
}

// fetchEpisodePlan downloads one pinned survivoR snapshot and plans the episode.
func fetchEpisodePlan(ctx context.Context, call apiCall, instancePath string, season, number int, rules importRules) (episodePlan, error) {
	base, revision, err := survivorRevision(ctx)
	if err != nil {
		return episodePlan{}, err
	}
	data, err := fetchSurvivorEpisodeFrom(ctx, base, season, number)
	if err != nil {
		return episodePlan{}, err
	}
	roster, err := loadRoster(ctx, call, instancePath)
	if err != nil {
		return episodePlan{}, err
	}
	return planEpisode(data, revision, roster, rules), nil
}
