package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Episode results from the Survivor wiki (survivor.fandom.com), used when survivoR is behind. The wiki is
// turned into survivoR-shaped tables so planEpisode applies the same rules, holds and reviews; survivoR
// replaces a wiki import once it has the episode.

// wikiSettle is how long a wiki page must go unedited before it's trusted (vandalism, half-done edits).
const wikiSettle = time.Hour

var (
	wikiExit     = regexp.MustCompile(`\b(\d+(?:st|nd|rd|th) Voted Out|Quit|Medically Evacuated|Evacuated|Ejected)\b`)
	wikiVotedOut = regexp.MustCompile(`VOTED OUT:[^}]*?link=([^|\]]+)`)
	// Either "'''Immunity Challenge:'''" or a <tabber> tab "|-|Immunity=".
	wikiChallenge = regexp.MustCompile(`'''((?:Reward|Immunity)(?:\s*(?:/|and|&)\s*(?:Reward|Immunity))?) Challenge:'''|(?m)(?:<tabber>|^\|-\|)\s*((?:Reward|Immunity)(?:\s*(?:/|and|&)\s*(?:Reward|Immunity))?)\s*=`)
	wikiWinners   = regexp.MustCompile(`'''Winners?.*?:'''(.*)`) // also "'''Winners ''(in order of finish)'':'''"
	wikiTribe     = regexp.MustCompile(`\{\{[Tt]ribeicon\|([^}|]+)`)
	wikiPerson    = regexp.MustCompile(`\{\{[Tt]ribehl\d*\|[^|}]+\|([^|}]+)`)
	wikiLink      = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]*)?\]\]`)
)

type wikiPage struct {
	Title, Text string
	RevID       int
	Edited      time.Time
}

// wikiFetch reads a page's latest revision through the MediaWiki API. PROBST_WIKI_URL overrides the API URL.
func wikiFetch(ctx context.Context, params url.Values) ([]byte, error) {
	api := "https://survivor.fandom.com/api.php"
	if v := os.Getenv("PROBST_WIKI_URL"); v != "" {
		api = v
	}
	params.Set("format", "json")
	params.Set("formatversion", "2")
	req, err := http.NewRequestWithContext(ctx, "GET", api+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "probst (castaway league bot; https://castaway.bry-guy.net)")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("survivor wiki: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || resp.StatusCode != 200 {
		return nil, fmt.Errorf("survivor wiki: HTTP %d %v", resp.StatusCode, err)
	}
	return body, nil
}

func wikiPageFetch(ctx context.Context, title string) (wikiPage, error) {
	body, err := wikiFetch(ctx, url.Values{"action": {"query"}, "prop": {"revisions"}, "titles": {title}, "rvprop": {"content|timestamp|ids"}, "rvslots": {"main"}, "redirects": {"1"}})
	if err != nil {
		return wikiPage{}, err
	}
	var res struct {
		Query struct {
			Pages []struct {
				Title     string `json:"title"`
				Missing   bool   `json:"missing"`
				Revisions []struct {
					RevID     int       `json:"revid"`
					Timestamp time.Time `json:"timestamp"`
					Slots     struct {
						Main struct {
							Content string `json:"content"`
						} `json:"main"`
					} `json:"slots"`
				} `json:"revisions"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &res); err != nil || len(res.Query.Pages) == 0 {
		return wikiPage{}, fmt.Errorf("survivor wiki %q: unreadable response", title)
	}
	p := res.Query.Pages[0]
	if p.Missing || len(p.Revisions) == 0 {
		return wikiPage{Title: title}, nil
	}
	r := p.Revisions[0]
	return wikiPage{Title: p.Title, Text: r.Slots.Main.Content, RevID: r.RevID, Edited: r.Timestamp}, nil
}

// wikiEpisodeTitle expands {{Ep|5106}} to the episode's page title.
func wikiEpisodeTitle(ctx context.Context, season, episode int) (string, error) {
	body, err := wikiFetch(ctx, url.Values{"action": {"expandtemplates"}, "text": {fmt.Sprintf("{{Ep|%d%02d}}", season, episode)}, "prop": {"wikitext"}})
	if err != nil {
		return "", err
	}
	var res struct {
		Expand struct {
			Wikitext string `json:"wikitext"`
		} `json:"expandtemplates"`
	}
	m := wikiLink.FindStringSubmatch(string(body))
	if json.Unmarshal(body, &res) == nil {
		m = wikiLink.FindStringSubmatch(res.Expand.Wikitext)
	}
	if m == nil {
		return "", fmt.Errorf("survivor wiki: no page for episode %d", episode)
	}
	return m[1], nil
}

// fetchWikiPlan plans an episode from the wiki's episode page and season episode guide.
func fetchWikiPlan(ctx context.Context, call apiCall, instancePath string, season, episode int, rules importRules, now time.Time) (episodePlan, error) {
	title, err := wikiEpisodeTitle(ctx, season, episode)
	if err != nil {
		return episodePlan{}, err
	}
	page, err := wikiPageFetch(ctx, title)
	if err != nil {
		return episodePlan{}, err
	}
	guide, err := wikiPageFetch(ctx, fmt.Sprintf("Template:S%depguide", season))
	if err != nil {
		return episodePlan{}, err
	}
	roster, err := loadRoster(ctx, call, instancePath)
	if err != nil {
		return episodePlan{}, err
	}
	return planWikiEpisode(season, episode, page, guide, roster, rules, now), nil
}

func planWikiEpisode(season, episode int, page, guide wikiPage, roster []*contestant, rules importRules, now time.Time) episodePlan {
	data, why := wikiEpisodeData(season, episode, page.Text, guide.Text, len(roster))
	revision := fmt.Sprintf("wiki:%d", page.RevID)
	if why == "" && now.Sub(page.Edited) < wikiSettle { // the guide is edited all the time; it only gives places
		why = fmt.Sprintf("%q was edited in the last hour", page.Title)
	}
	if why != "" {
		return episodePlan{Episode: episode, Revision: revision, Status: "incomplete", Why: why}
	}
	p := planEpisode(data, revision, roster, rules)
	for i := range p.Challenges { // its own keys, so survivoR's import can tell them apart and replace them
		p.Challenges[i].Key = strings.Replace(p.Challenges[i].Key, "survivor-", "wiki-", 1)
	}
	for i := range p.Champions {
		p.Champions[i].Key = strings.Replace(p.Champions[i].Key, "survivor-", "wiki-", 1)
	}
	return p
}

// wikiEpisodeData turns the wiki into survivoR tables; why is set when the wiki isn't complete yet.
func wikiEpisodeData(season, episode int, page, guide string, castSize int) (survivorEpisode, string) {
	data := survivorEpisode{Season: season, Episode: episode, Tables: map[string][]map[string]any{}}
	if page == "" {
		return data, "the wiki has no page for the episode yet"
	}

	// Exits, in order, from the season's episode guide; places count down from the cast size.
	code := func(ep int) string { return fmt.Sprintf("{{Ep|%d%02d}}", season, ep) }
	start := strings.Index(guide, code(episode))
	if start < 0 {
		return data, "the wiki's episode guide doesn't list the episode yet"
	}
	segment := guide[start:]
	if end := strings.Index(segment[1:], "{{Ep|"); end >= 0 {
		segment = segment[:end+1]
	}
	if end := strings.Index(segment, "'''Notes:'''"); end >= 0 {
		segment = segment[:end]
	}
	before := len(wikiExit.FindAllString(guide[:start], -1))
	exits := wikiExit.FindAllString(segment, -1)
	names := wikiVotedOut.FindAllStringSubmatch(page, -1)
	switch {
	case len(names) == 0:
		return data, "the wiki doesn't show who was voted out yet"
	case len(exits) != len(names):
		return data, fmt.Sprintf("the wiki's episode guide lists %d exits but the episode page shows %d votes; wait for survivoR", len(exits), len(names))
	}
	for i, m := range names {
		id := "wiki:" + strings.TrimSpace(m[1])
		data.Tables["castaways"] = append(data.Tables["castaways"], map[string]any{"castaway_id": id, "full_name": strings.TrimSpace(m[1]), "place": float64(castSize - before - i)})
		data.Tables["boot_order"] = append(data.Tables["boot_order"], map[string]any{"castaway_id": id, "castaway": strings.TrimSpace(m[1]), "result": strings.ToLower(exits[i])})
	}

	// Challenges: every one described on the episode page needs its winners.
	section := page
	if i := strings.Index(section, "==Challenges=="); i >= 0 {
		section = section[i+len("==Challenges=="):]
		if j := strings.Index(section, "\n=="); j >= 0 && !strings.HasPrefix(section[j:], "\n===") {
			section = section[:j]
		}
	} else {
		return data, "the wiki doesn't list the episode's challenges yet"
	}
	blocks := wikiChallenge.FindAllStringSubmatchIndex(section, -1)
	if len(blocks) == 0 {
		return data, "the wiki doesn't list the episode's challenges yet"
	}
	for n, b := range blocks {
		id := float64(n + 1)
		lo, hi := b[2], b[3]
		if lo < 0 {
			lo, hi = b[4], b[5]
		}
		kind := strings.ToLower(section[lo:hi])
		body := section[b[1]:]
		if n+1 < len(blocks) {
			body = section[b[1]:blocks[n+1][0]]
		}
		data.Tables["challenge_description"] = append(data.Tables["challenge_description"], map[string]any{"challenge_id": id, "name": section[lo:hi]})
		w := wikiWinners.FindStringSubmatch(body)
		if w == nil || strings.Contains(w[1], "TBD") {
			continue // no results: planEpisode reports the episode incomplete
		}
		if tribes := wikiTribe.FindAllStringSubmatch(w[1], -1); len(tribes) > 0 {
			for _, t := range tribes {
				name := strings.TrimSpace(t[1])
				data.Tables["challenge_results"] = append(data.Tables["challenge_results"], map[string]any{"challenge_id": id, "challenge_type": kind, "outcome_type": "Tribal", "tribe": strings.ToUpper(name[:1]) + name[1:], "won": 1.0})
			}
			// The wiki names only winners; a losing row keeps "every tribe won" from looking true.
			data.Tables["challenge_results"] = append(data.Tables["challenge_results"], map[string]any{"challenge_id": id, "challenge_type": kind, "outcome_type": "Tribal", "tribe": "(the rest)", "won": 0.0})
			continue
		}
		for _, l := range append(wikiPerson.FindAllStringSubmatch(w[1], -1), wikiLink.FindAllStringSubmatch(w[1], -1)...) {
			name := strings.TrimSpace(l[1])
			if strings.Contains(name, ":") {
				continue // File:, Category: links
			}
			cid := "wiki:" + name
			if !wikiHasCastaway(data, cid) {
				data.Tables["castaways"] = append(data.Tables["castaways"], map[string]any{"castaway_id": cid, "full_name": name})
			}
			data.Tables["challenge_results"] = append(data.Tables["challenge_results"], map[string]any{"challenge_id": id, "challenge_type": kind, "outcome_type": "Individual", "castaway_id": cid, "won": 1.0})
		}
	}
	data.Tables["episodes"] = []map[string]any{{"episode": float64(episode)}}
	return data, ""
}

func wikiHasCastaway(data survivorEpisode, id string) bool {
	for _, c := range data.Tables["castaways"] {
		if c["castaway_id"] == id {
			return true
		}
	}
	return false
}
