package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// seasonFile is seasons/NN.yaml: the whole season in one file. See plans/season-schedule.md.
// Values of "TBD" (or tbd: true) are placeholders: shown on the timeline, never acted on.
type seasonFile struct {
	Season   int    `yaml:"season"`
	Instance string `yaml:"instance"`
	Channel  string `yaml:"channel"`
	Ping     string `yaml:"ping"`
	// Templates is the template directory, relative to the file (default: <file>-templates).
	Templates string `yaml:"templates"`
	Episodes  struct {
		First  string   `yaml:"first"`
		Count  int      `yaml:"count"`
		Skip   []string `yaml:"skip"`
		Finale int      `yaml:"finale"`
	} `yaml:"episodes"`
	Draft struct {
		Opens     string         `yaml:"opens"`
		SoftClose string         `yaml:"soft_close"`
		Buffs     map[string]int `yaml:"buffs"`
	} `yaml:"draft"`
	Scoring struct {
		Tribe        map[string]any `yaml:"tribe"`
		Champion     map[string]any `yaml:"champion"`
		MergeEpisode any            `yaml:"merge_episode"`
	} `yaml:"scoring"`
	Weekly struct {
		// DiscussionThread opens each episode's public discussion thread at its air time (discussion.go).
		DiscussionThread bool `yaml:"discussion_thread"`
		Results          struct {
			Due   string   `yaml:"due"`
			Needs []string `yaml:"needs"`
		} `yaml:"results"`
		Game       planGame `yaml:"game"`
		ScoresPost planPost `yaml:"scores_post"`
		// Reminder is the game-day "last call" post, e.g. "+7d 10:00"; drafted for approval when the game opens.
		Reminder struct {
			At string `yaml:"at"`
		} `yaml:"reminder"`
	} `yaml:"weekly"`
	Weeks map[int]struct {
		Game       any       `yaml:"game"`
		ScoresPost *planPost `yaml:"scores_post"`
	} `yaml:"weeks"`
	Posts      []planPost       `yaml:"posts"`
	Automation automationConfig `yaml:"automation"`
}

type planGame struct {
	Opens   string `yaml:"opens"`
	Closes  string `yaml:"closes"`
	Resolve string `yaml:"resolve"`
}

type planPost struct {
	Name     string              `yaml:"name"`
	At       any                 `yaml:"at"`
	Template string              `yaml:"template"`
	Sent     bool                `yaml:"sent"`
	Approved bool                `yaml:"approved"`
	TBD      bool                `yaml:"tbd"`
	Ping     any                 `yaml:"ping"`
	Callouts []map[string]string `yaml:"callouts"`
}

type planItem struct {
	At     time.Time // zero = unscheduled placeholder
	Week   int
	What   string
	Status string // ✓ done · ⚠ placeholder/unapproved · • upcoming
}

func isTBD(v any) bool {
	s, ok := v.(string)
	return v == nil || (ok && strings.EqualFold(strings.TrimSpace(s), "TBD"))
}

// planTime resolves "2006-01-02 15:04", "15:04" (on base's date) or "+Nd 15:04" (N days after base).
func planTime(v string, base time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.ParseInLocation("2006-01-02 15:04", v, eastern()); err == nil {
		return t, nil
	}
	days := 0
	if strings.HasPrefix(v, "+") {
		d, rest, ok := strings.Cut(v[1:], "d ")
		n, err := strconv.Atoi(d)
		if !ok || err != nil {
			return time.Time{}, fmt.Errorf("bad relative time %q (want +Nd HH:MM)", v)
		}
		days, v = n, rest
	}
	clock, err := time.Parse("15:04", v)
	if err != nil || base.IsZero() {
		return time.Time{}, fmt.Errorf("bad time %q", v)
	}
	b := base.In(eastern())
	return time.Date(b.Year(), b.Month(), b.Day()+days, clock.Hour(), clock.Minute(), 0, 0, eastern()), nil
}

func loadSeasonFile(path string) (seasonFile, error) {
	var f seasonFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	return parseSeason(raw, path)
}

// parseSeason decodes a season file's contents (from disk or the synced database copy).
func parseSeason(raw []byte, name string) (seasonFile, error) {
	var f seasonFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("%s: %w", name, err)
	}
	return f, nil
}

// expandSeason turns the file into a dated timeline. now marks past items done only when the file says so
// (sent: true) or they're plain facts (episodes, draft windows).
func expandSeason(f seasonFile, now time.Time) ([]planItem, []time.Time, error) {
	var items []planItem
	add := func(at time.Time, week int, what, status string) {
		items = append(items, planItem{At: at, Week: week, What: what, Status: status})
	}
	fact := func(at time.Time) string {
		if at.Before(now) {
			return "✓"
		}
		return "•"
	}
	first, err := planTime(f.Episodes.First, time.Time{})
	if err != nil {
		return nil, nil, fmt.Errorf("episodes.first: %w", err)
	}
	skips := map[string]bool{}
	for _, s := range f.Episodes.Skip {
		skips[s] = true
	}
	var airs []time.Time
	for d := first; len(airs) < f.Episodes.Count; d = d.AddDate(0, 0, 7) {
		if !skips[d.Format("2006-01-02")] {
			airs = append(airs, d)
		}
	}
	for _, field := range []struct{ v, what string }{{f.Draft.Opens, "Draft opens"}, {f.Draft.SoftClose, "Draft soft-closes (peers' drafts visible)"}} {
		at, err := planTime(field.v, time.Time{})
		if err != nil {
			return nil, nil, fmt.Errorf("draft: %w", err)
		}
		add(at, 0, field.what, fact(at))
	}
	merge := 0
	if n, ok := f.Scoring.MergeEpisode.(int); ok {
		merge = n
	}
	for i, air := range airs {
		ep := i + 1
		label := fmt.Sprintf("Episode %d airs", ep)
		if ep == f.Episodes.Finale {
			label = fmt.Sprintf("Episode %d airs — FINALE", ep)
		}
		if ep == merge {
			label += " — MERGE (Tribes end, Pick Your Champion opens)"
		}
		add(air, ep, label, fact(air))
		if merge == 0 && isTBD(f.Scoring.MergeEpisode) && ep == len(airs)/2 {
			add(time.Time{}, ep, "Merge episode", "⚠ TBD (scoring.merge_episode)")
		}
		week := f.Weeks[ep]
		switch g := week.Game; {
		case g == "none":
		case week.Game == nil || isTBD(g):
			if week.Game == nil && ep > 1 {
				add(air, ep, "Game", "⚠ TBD (weeks."+strconv.Itoa(ep)+".game)")
			} else if week.Game != nil {
				add(air, ep, "Game", "⚠ TBD")
			}
		default:
			name := "Game"
			if m, ok := g.(map[string]any); ok {
				if t, ok := m["type"].(string); ok {
					name = "Game: " + t
					if n, ok := gameNames[t]; ok {
						name = "Game: " + n
					}
				}
				if m["tbd"] == true || isTBD(m["name"]) && m["name"] != nil {
					add(air, ep, name, "⚠ TBD")
					break
				}
			}
			opens, err1 := planTime(f.Weekly.Game.Opens, air)
			closes, err2 := planTime(f.Weekly.Game.Closes, air)
			if err1 != nil || err2 != nil {
				return nil, nil, fmt.Errorf("weekly.game: bad opens/closes")
			}
			add(opens, ep, name+" opens", fact(opens))
			add(closes, ep, name+" closes + awards", fact(closes))
		}
		post := f.Weekly.ScoresPost
		if week.ScoresPost != nil {
			if week.ScoresPost.At != nil {
				post.At = week.ScoresPost.At
			}
			post.Sent, post.Approved = week.ScoresPost.Sent, week.ScoresPost.Approved
			if week.ScoresPost.Template != "" {
				post.Template = week.ScoresPost.Template
			}
		}
		what := fmt.Sprintf("Week %d scores post", ep)
		if post.Template == "final" {
			what = "FINAL scores post"
		}
		if due, err := planTime(f.Weekly.Results.Due, air); err == nil && !post.Sent {
			add(due, ep, fmt.Sprintf("Episode %d results due (you)", ep), fact(due))
		}
		at, _ := post.At.(string)
		when, err := planTime(at, air)
		if err != nil {
			return nil, nil, fmt.Errorf("week %d scores_post.at: %w", ep, err)
		}
		add(when, ep, what, postStatus(post))
	}
	for _, p := range f.Posts {
		at, _ := p.At.(string)
		when, err := time.Time{}, error(nil)
		if !isTBD(p.At) {
			if when, err = planTime(at, time.Time{}); err != nil {
				return nil, nil, fmt.Errorf("post %s: %w", p.Name, err)
			}
		}
		add(when, weekOf(when, airs), "Post: "+p.Name, postStatus(p))
	}
	for i := range items { // group by the episode week each item falls in, so the timeline reads in order
		items[i].Week = weekOf(items[i].At, airs)
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.At.IsZero() != b.At.IsZero() {
			return !a.At.IsZero() // placeholders last
		}
		return a.At.Before(b.At)
	})
	return items, airs, nil
}

func postStatus(p planPost) string {
	switch {
	case p.Sent:
		return "✓ sent"
	case p.TBD || isTBD(p.At):
		return "⚠ TBD"
	case !p.Approved:
		return "⚠ needs approval"
	default:
		return "• approved"
	}
}

func weekOf(t time.Time, airs []time.Time) int {
	week := 0
	for i, a := range airs {
		if !t.IsZero() && !t.Before(a) {
			week = i + 1
		}
	}
	return week
}

func printTimeline(w io.Writer, items []planItem) {
	lastWeek := -1
	for _, it := range items {
		if it.At.IsZero() {
			if lastWeek != -2 {
				fmt.Fprintln(w, "\nUnscheduled placeholders")
				lastWeek = -2
			}
			fmt.Fprintf(w, "  %-22s %-60s %s\n", "—", it.What, it.Status)
			continue
		}
		if wk := it.Week; wk != lastWeek && lastWeek != -2 {
			if wk == 0 {
				fmt.Fprintln(w, "\nPreseason")
			} else {
				fmt.Fprintf(w, "\nWeek %d\n", wk)
			}
			lastWeek = wk
		}
		fmt.Fprintf(w, "  %-22s %-60s %s\n", it.At.In(eastern()).Format("Mon Jan 02 3:04pm"), it.What, it.Status)
	}
}

func newSeasonCommand(call apiCall, instance, guild *string, yes *bool) *cobra.Command {
	season := &cobra.Command{Use: "season", Short: "Declarative season schedule (seasons/NN.yaml)"}
	season.AddCommand(&cobra.Command{
		Use:   "plan FILE",
		Short: "Show the season timeline and how it differs from the live instance (read-only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			f, err := loadSeasonFile(a[0])
			if err != nil {
				return err
			}
			items, airs, err := expandSeason(f, time.Now())
			if err != nil {
				return err
			}
			out := c.OutOrStdout()
			fmt.Fprintf(out, "Season %d — timeline (ET)\n", f.Season)
			printTimeline(out, items)
			diffs, err := seasonDiff(c.Context(), call, f, airs)
			if err != nil {
				return fmt.Errorf("compare with live instance: %w", err)
			}
			fmt.Fprintln(out, "\nDifferences from the live instance")
			if len(diffs) == 0 {
				fmt.Fprintln(out, "  none")
			}
			for _, d := range diffs {
				fmt.Fprintln(out, "  "+d)
			}
			return nil
		},
	})
	addSeasonPostCommand(season, call, guild, yes)
	addSeasonApplyCommand(season, call, yes)
	addSeasonCheckCommand(season, call)
	addSeasonReconcileCommand(season, call, instance)
	addSeasonSyncCommand(season, call, yes)
	addSeasonMergeCommand(season, call, yes)
	return season
}

// seasonDiff compares what the live instance can report today: episode air times and unsent announcements.
func seasonDiff(ctx context.Context, call apiCall, f seasonFile, airs []time.Time) ([]string, error) {
	path := "/instances/" + url.PathEscape(f.Instance)
	live, err := loadEpisodes(ctx, call, path)
	if err != nil {
		return nil, err
	}
	byNum := map[int]time.Time{}
	for _, e := range live {
		byNum[e.Number] = e.AirsAt
	}
	var diffs []string
	for i, air := range airs {
		got, ok := byNum[i+1]
		switch {
		case !ok:
			diffs = append(diffs, fmt.Sprintf("Episode %d: missing live (file: %s)", i+1, air.Format("Mon Jan 02 3:04pm")))
		case !got.Equal(air):
			diffs = append(diffs, fmt.Sprintf("Episode %d: live airs %s, file says %s", i+1, got.In(eastern()).Format("Mon Jan 02 3:04pm"), air.Format("Mon Jan 02 3:04pm")))
		}
	}
	for n := range byNum {
		if n > len(airs) {
			diffs = append(diffs, fmt.Sprintf("Episode %d: live only (not in file)", n))
		}
	}
	anns, err := loadAnnouncements(ctx, call, path)
	if err != nil {
		return nil, err
	}
	for _, an := range anns {
		if an.Status != "sent" {
			diffs = append(diffs, fmt.Sprintf("Announcement %s (%s, due %s): live only (not in file)", an.RequestKey, an.Status, an.DueAt.In(eastern()).Format("Mon Jan 02 3:04pm")))
		}
	}
	sort.Strings(diffs)
	return diffs, nil
}

func loadAnnouncements(ctx context.Context, call apiCall, instancePath string) ([]announcement, error) {
	var res struct {
		Announcements []announcement `json:"announcements"`
	}
	return res.Announcements, call(ctx, "GET", instancePath+"/announcements", nil, &res)
}
