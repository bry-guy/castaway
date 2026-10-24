package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// gameNames are the games' display names (the season's templates own every other word).
var gameNames = map[string]string{"press_the_button": "Press the Button", "castawordle": "Castawordle", "spell_it_out": "Island Scramble", "inside_track": "Inside Track", "free_the_block": "Free the Block"}

// rankLine is one ranked player in a scores post.
type rankLine struct {
	Mention string
	Total   int
}

type scoresPost struct {
	Season, Week                         int
	Intro, Boots, Leader, Gainer, Slider string
	Top                                  []rankLine
	Last                                 rankLine
	Site, NextGame, GameScore            string
	Facts                                scoresFacts
}

func joinNames(names []string) string {
	if len(names) > 3 {
		return fmt.Sprintf("%d players", len(names))
	}
	if n := len(names); n > 1 {
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	}
	return strings.Join(names, "")
}

// buildScoresPost fills the weekly post from the board now and as it stood at the previous post. Only the top
// three and last place are mentioned (and pinged); callouts name players without pinging.
func buildScoresPost(t templateSet, season, week int, now, prev []scoreRow, booted []string, site, nextGame string) (scoresPost, error) {
	var rows []scoreRow
	before := map[string]int{}
	for _, r := range prev {
		before[r.ID] = r.Total
	}
	for _, r := range now {
		if r.HasDraft {
			rows = append(rows, r)
		}
	}
	if len(rows) < 2 {
		return scoresPost{}, fmt.Errorf("need at least two players with drafts")
	}
	// Stable: equal totals keep the server's tiebreak order (draft points, then earliest draft).
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Total > rows[j].Total })
	gain := func(r scoreRow) int { return r.Total - before[r.ID] }
	who := func(match func(scoreRow) bool) []string {
		var names []string
		for _, r := range rows {
			if match(r) {
				names = append(names, r.Name)
			}
		}
		return names
	}
	best, worst := gain(rows[0]), gain(rows[0])
	for _, r := range rows {
		best, worst = max(best, gain(r)), min(worst, gain(r))
	}
	leaders := who(func(r scoreRow) bool { return r.Total == rows[0].Total })
	gainers := who(func(r scoreRow) bool { return gain(r) == best })
	sliders := who(func(r scoreRow) bool { return gain(r) == worst })
	p := scoresPost{Season: season, Week: week, Site: site, NextGame: nextGame}
	p.Facts = scoresFacts{
		LeaderNames: leaders, LeaderTotal: rows[0].Total, GainerNames: gainers, Gain: best,
		SliderNames: sliders, Slide: -worst, NobodyMoved: best == worst && best == 0,
	}
	var errs []error
	line := func(name string, names []string, n int) string {
		text, err := t.line(name, week, lineData{Names: joinNames(names), N: n, Plural: len(names) > 1})
		errs = append(errs, err)
		return text
	}
	p.Intro = line("intro", nil, 0)
	p.Leader = line("leader", leaders, rows[0].Total)
	switch {
	case best == worst && best == 0: // nobody moved: one line instead of three ties
		p.Leader += " " + line("nobody-moved", nil, 0)
	case best == worst: // everybody moved the same: nobody to praise or rib
		p.Leader += " " + line("all-moved", nil, best)
	case worst < 0:
		p.Gainer, p.Slider = line("gainer", gainers, best), line("slider", sliders, -worst)
	default: // nobody lost points: rib the slowest climber instead
		p.Gainer, p.Slider = line("gainer", gainers, best), line("slow", sliders, worst)
	}
	for _, r := range rows[:min(3, len(rows)-1)] {
		p.Top = append(p.Top, rankLine{mention(r), r.Total})
	}
	last := rows[len(rows)-1]
	p.Last = rankLine{mention(last), last.Total}
	if len(booted) > 0 {
		if len(booted) > 1 {
			p.Boots = line("boot-many", booted, 0)
		} else {
			p.Boots = line("boot", booted, 0)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return scoresPost{}, err
	}
	return p, nil
}

// renderScoresPost fills the season's scores.md.
func renderScoresPost(t templateSet, p scoresPost) (string, error) {
	text, err := t.render("scores.md", p)
	return text + "\n", err
}

// weekPostTime is when week's scores post goes out according to the file.
func weekPostTime(f seasonFile, airs []time.Time, week int) (time.Time, error) {
	if week < 1 || week > len(airs) {
		return time.Time{}, fmt.Errorf("week %d is outside the season", week)
	}
	post := f.Weekly.ScoresPost
	if w := f.Weeks[week]; w.ScoresPost != nil && w.ScoresPost.At != nil {
		post.At = w.ScoresPost.At
	}
	at, _ := post.At.(string)
	return planTime(at, airs[week-1])
}

// weekGame is a week's game type, or "" when the file has none, TBD, or a placeholder.
func weekGame(f seasonFile, week int) string {
	m, ok := f.Weeks[week].Game.(map[string]any)
	if !ok || m["tbd"] == true {
		return ""
	}
	t, _ := m["type"].(string)
	return t
}

func scoresKey(season, week int) string { return fmt.Sprintf("s%d-week-%d-scores", season, week) }

// seasonTemplates loads a season file's template directory.
func seasonTemplates(file string, f seasonFile) (templateSet, error) {
	return loadTemplates(templateDir(file, f))
}

func addSeasonPostCommand(season *cobra.Command, call apiCall, guild *string, yes *bool) {
	var week int
	var body string
	cmd := &cobra.Command{
		Use:   "post FILE --week N",
		Short: "Render a week's scores post from the template; --yes saves it for admin approval (DM) at the file's time",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			f, err := loadSeasonFile(a[0])
			if err != nil {
				return err
			}
			tmpl, err := seasonTemplates(a[0], f)
			if err != nil {
				return err
			}
			_, airs, err := expandSeason(f, time.Now())
			if err != nil {
				return err
			}
			at, err := weekPostTime(f, airs, week)
			if err != nil {
				return err
			}
			var text string
			if body != "" { // an edited draft from `probst scores generate`
				raw, err := os.ReadFile(body) // #nosec G304 -- operator-chosen local draft
				if err != nil {
					return err
				}
				text = strings.TrimSpace(string(raw)) + "\n"
			} else if text, err = composeScoresPost(c.Context(), call, f, airs, week, tmpl); err != nil {
				return err
			}
			out := c.OutOrStdout()
			if previewUnscored && *yes {
				return fmt.Errorf("--preview is for dry runs only")
			}
			key := scoresKey(f.Season, week)
			if !*yes {
				_, err = fmt.Fprintf(out, "Dry run — %s, sends %s ET (pings the top 3 and last place):\n\n%s\nRe-run with --yes to save and schedule it.\n", key, at.Format("Mon Jan 02 3:04pm"), text)
				return err
			}
			path := "/instances/" + url.PathEscape(f.Instance)
			if err := saveDraftAnnouncement(c.Context(), call, path, cmp.Or(*guild, f.Automation.Guild), f.Channel, key, text); err != nil {
				return err
			}
			found, err := findAnnouncement(c.Context(), call, path, key)
			if err != nil {
				return err
			}
			// Saved behind the approval gate: every admin gets a DM and it sends at its time once one replies "yes".
			if err := call(c.Context(), "PUT", path+"/announcements/"+url.PathEscape(found.ID)+"/approval", map[string]any{"send_at": at.UTC().Format(time.RFC3339)}, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "Saved %s for %s ET, waiting for approval: admins get a DM and it posts once one replies \"yes\". Re-run before then to refresh it (that re-asks).\n", key, at.Format("Mon Jan 02 3:04pm"))
			return err
		},
	}
	cmd.Flags().IntVar(&week, "week", 0, "Week N (scores through Episode N)")
	cmd.Flags().BoolVar(&previewUnscored, "preview", false, "Dry run before the game is scored: placeholder for its results")
	cmd.Flags().StringVar(&body, "body", "", "Post this edited draft instead of rendering the template")
	season.AddCommand(cmd)
}

// saveDraftAnnouncement saves text as an unsent draft under key (its mentions ping), or replaces an unsent draft's text.
func saveDraftAnnouncement(ctx context.Context, call apiCall, path, guild, channel, key, text string) error {
	existing, err := findAnnouncement(ctx, call, path, key)
	switch {
	case err == nil && existing.Status == "sent":
		return fmt.Errorf("%s was already sent", key)
	case err == nil:
		return call(ctx, "PUT", path+"/announcements/"+url.PathEscape(existing.ID)+"/body", map[string]string{"body": text}, nil)
	default:
		return call(ctx, "POST", path+"/announcements", map[string]any{
			"guild_id": guild, "channel_id": channel, "request_key": key, "body": text, "draft": true, "notify_users": true,
		}, nil)
	}
}

// previewUnscored lets a dry run (`season post --preview`) render before the week's game is scored.
var previewUnscored bool

func composeScoresPost(ctx context.Context, call apiCall, f seasonFile, airs []time.Time, week int, tmpl templateSet) (string, error) {
	p, err := composeScoresData(ctx, call, f, airs, week, tmpl)
	if err != nil {
		return "", err
	}
	return renderScoresPost(tmpl, p)
}

// composeScoresData gathers the week's standings, movers, boots and next game from the API.
func composeScoresData(ctx context.Context, call apiCall, f seasonFile, airs []time.Time, week int, tmpl templateSet) (scoresPost, error) {
	path := "/instances/" + url.PathEscape(f.Instance)
	var now, prev struct {
		Leaderboard []scoreRow `json:"leaderboard"`
	}
	if err := call(ctx, "GET", path+"/leaderboard", nil, &now); err != nil {
		return scoresPost{}, err
	}
	since := time.Time{}
	if week > 1 {
		t, err := weekPostTime(f, airs, week-1)
		if err != nil {
			return scoresPost{}, err
		}
		since = t
		if err := call(ctx, "GET", path+"/leaderboard?at="+url.QueryEscape(since.UTC().Format(time.RFC3339)), nil, &prev); err != nil {
			return scoresPost{}, err
		}
	}
	var outs struct {
		Outcomes []struct {
			Name      string    `json:"contestant_name"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"outcomes"`
	}
	if err := call(ctx, "GET", path+"/outcomes", nil, &outs); err != nil {
		return scoresPost{}, err
	}
	var booted []string
	for _, o := range outs.Outcomes {
		if o.Name != "" && o.UpdatedAt.After(since) {
			booted = append(booted, fmt.Sprintf("||%-14s||", "  "+shortName(o.Name)))
		}
	}
	next, err := nextGameLine(tmpl, f, airs, week)
	if err != nil {
		return scoresPost{}, err
	}
	p, err := buildScoresPost(tmpl, f.Season, week, now.Leaderboard, prev.Leaderboard, booted, siteURL, next)
	if err != nil {
		return p, err
	}
	p.GameScore, err = gameScore(ctx, call, path, tmpl, f, week)
	if err != nil && previewUnscored && strings.Contains(err.Error(), "isn't scored yet") {
		p.GameScore, err = gameScorePlaceholder(weekGame(f, week)), nil
	}
	return p, err
}

func addSeasonApplyCommand(season *cobra.Command, call apiCall, yes *bool) {
	var only int
	cmd := &cobra.Command{
		Use:   "apply FILE",
		Short: "Create or reschedule the file's games (dry run unless --yes); placeholders are skipped",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			f, err := loadSeasonFile(a[0])
			if err != nil {
				return err
			}
			_, airs, err := expandSeason(f, time.Now())
			if err != nil {
				return err
			}
			out := c.OutOrStdout()
			for week := 1; week <= len(airs); week++ {
				game := weekGame(f, week)
				if game == "" || only > 0 && week != only {
					continue
				}
				opens, err1 := planTime(f.Weekly.Game.Opens, airs[week-1])
				closes, err2 := planTime(f.Weekly.Game.Closes, airs[week-1])
				if err1 != nil || err2 != nil {
					return fmt.Errorf("weekly.game: bad opens/closes")
				}
				line := fmt.Sprintf("Week %d %s: %s → %s ET", week, gameNames[game], opens.Format("Mon Jan 02 3:04pm"), closes.Format("Mon Jan 02 3:04pm"))
				switch game {
				case "press_the_button":
					if !*yes {
						fmt.Fprintln(out, "would apply "+line)
						continue
					}
					body := map[string]any{"episode_number": week, "opens_at": opens.UTC().Format(time.RFC3339), "cutoff_at": closes.UTC().Format(time.RFC3339)}
					if err := call(c.Context(), "POST", "/instances/"+url.PathEscape(f.Instance)+"/button-games", body, nil); err != nil {
						return fmt.Errorf("%s: %w", line, err)
					}
					fmt.Fprintln(out, "applied "+line)
				case "spell_it_out":
					// phrase_file (relative to the season file) keeps the phrase out of the committed plan.
					m, _ := f.Weeks[week].Game.(map[string]any)
					phraseFile, _ := m["phrase_file"].(string)
					decoys, _ := m["decoys"].(int)
					if phraseFile == "" {
						return fmt.Errorf("week %d: spell_it_out needs phrase_file", week)
					}
					if !*yes {
						fmt.Fprintf(out, "would apply %s (phrase from %s, %d decoys)\n", line, phraseFile, decoys)
						continue
					}
					phrase, err := os.ReadFile(filepath.Join(filepath.Dir(a[0]), phraseFile))
					if err != nil {
						return fmt.Errorf("week %d: %w", week, err)
					}
					body := map[string]any{"episode_number": week, "phrase": strings.TrimSpace(string(phrase)), "decoys": decoys,
						"opens_at": opens.UTC().Format(time.RFC3339), "cutoff_at": closes.UTC().Format(time.RFC3339)}
					if err := call(c.Context(), "POST", "/instances/"+url.PathEscape(f.Instance)+"/scramble-games", body, nil); err != nil {
						return fmt.Errorf("%s: %w", line, err)
					}
					fmt.Fprintln(out, "applied "+line)
				case "inside_track", "free_the_block":
					if !*yes {
						fmt.Fprintln(out, "would apply "+line)
						continue
					}
					path := map[string]string{"inside_track": "track-games", "free_the_block": "free-the-block-games"}[game]
					body := map[string]any{"episode_number": week, "opens_at": opens.UTC().Format(time.RFC3339), "cutoff_at": closes.UTC().Format(time.RFC3339)}
					if err := call(c.Context(), "POST", "/instances/"+url.PathEscape(f.Instance)+"/"+path, body, nil); err != nil {
						return fmt.Errorf("%s: %w", line, err)
					}
					fmt.Fprintln(out, "applied "+line)
				case "castawordle":
					fmt.Fprintln(out, "skipped "+line+" (set up with `probst castawordle week`, which needs the answer)")
				default:
					return fmt.Errorf("week %d: unknown game type %q", week, game)
				}
			}
			if !*yes {
				fmt.Fprintln(out, "Dry run — re-run with --yes to apply.")
			}
			if only > 0 {
				return nil
			}
			return applyChampionRound(c, call, f, airs, *yes)
		},
	}
	cmd.Flags().IntVar(&only, "week", 0, "Apply only week N's game (games already played can't be rescheduled)")
	season.AddCommand(cmd)
}

// nextGameLine announces next week's game, which opens with the episode the post follows.
func nextGameLine(t templateSet, f seasonFile, airs []time.Time, week int) (string, error) {
	game := weekGame(f, week+1)
	if gameNames[game] == "" || week >= len(airs) {
		return "", nil
	}
	opens, err := planTime(f.Weekly.Game.Opens, airs[week])
	if err != nil {
		return "", err
	}
	return t.render("next-game.md", map[string]string{"Type": game, "Name": gameNames[game], "Site": siteURL,
		"Opens": shortTime(opens)})
}

// gameScorePlaceholder stands in for a game's results section until the game is scored.
func gameScorePlaceholder(game string) string {
	return fmt.Sprintf("[%s results go here once it's scored]", gameNames[game])
}

// gameScore is the week's game results section, from games/<type>.md and the server's scored results.
func gameScore(ctx context.Context, call apiCall, path string, t templateSet, f seasonFile, week int) (string, error) {
	game := weekGame(f, week)
	if gameNames[game] == "" {
		return "", nil
	}
	var res struct {
		Players []map[string]any `json:"players"`
		Tribes  []map[string]any `json:"tribes"`
	}
	if err := call(ctx, "GET", fmt.Sprintf("%s/game-results/%d", path, week), nil, &res); err != nil {
		return "", fmt.Errorf("week %d %s results: %w", week, gameNames[game], err)
	}
	players, tribes := []gameRow{}, []tribeRow{}
	for _, m := range res.Players {
		players = append(players, gameRow{Name: str(m["name"]), TribeEmoji: str(m["tribe_emoji"]), Tribe: str(m["tribe"]), Points: int(num(m["points"])),
			Presses: int(num(m["presses"])), First: m["first"] == true, Last: m["last"] == true, StreakDays: int(num(m["streak_days"])),
			Place: int(num(m["place"])), Seconds: int(num(m["seconds"])), WrongChecks: int(num(m["wrong_checks"])),
			HintPoints: int(num(m["hint_points"])), CleanPoints: int(num(m["clean_points"])),
			Score: int(num(m["score"])), Cups: int(num(m["cups"])), BallsLeft: int(num(m["balls_left"])),
			Guesses: int(num(m["guesses"])), Solved: m["solved"] == true, Moves: int(num(m["moves"])),
			RankRule: str(m["rank_rule"]), RankPoints: int(num(m["rank_points"])), VolumePoints: int(num(m["volume_points"])), StreakPoints: int(num(m["streak_points"]))})
	}
	for _, m := range res.Tribes {
		tribes = append(tribes, tribeRow{Name: str(m["name"]), Emoji: str(m["emoji"]), Value: fnum(m["value"])})
	}
	return renderGameScore(t, game, week, players, tribes)
}

// renderGameScore fills games/<game>.md from the game's results.
func renderGameScore(t templateSet, game string, week int, players []gameRow, tribes []tribeRow) (string, error) {
	return t.render("games/"+game+".md", map[string]any{"Name": gameNames[game], "Week": week, "Players": players, "Tribes": tribes, "Rules": buttonRules(players)})
}

// ruleGroup is the players who hit one Press the Button rule together (a tie, a streak length, ...).
type ruleGroup struct {
	Names          string
	Count, Presses int // Count: players in the group
	Days, Points   int
}

// buttonRules groups players by the scoring rules they hit, in the order games/press_the_button.md shows them.
func buttonRules(players []gameRow) map[string][]ruleGroup {
	out := map[string][]ruleGroup{}
	for _, rule := range []string{"most", "second", "tie", "least", "volume", "streak", "first", "last"} {
		out[rule] = nil // every rule present, so templates can range over rules nobody hit
	}
	add := func(rule string, key, days, points int, name string, presses int) {
		for i, g := range out[rule] {
			if g.Presses == key && g.Days == days && g.Points == points {
				out[rule][i].Names = g.Names + "\x00" + name
				out[rule][i].Count++
				return
			}
		}
		out[rule] = append(out[rule], ruleGroup{Names: name, Count: 1, Presses: presses, Days: days, Points: points})
	}
	for _, p := range players {
		if p.RankRule != "" {
			add(p.RankRule, p.Presses, 0, p.RankPoints, p.Name, p.Presses)
		}
		if p.VolumePoints > 0 {
			add("volume", p.Presses, 0, p.VolumePoints, p.Name, p.Presses)
		}
		if p.StreakPoints > 0 {
			add("streak", 0, p.StreakDays, p.StreakPoints, p.Name, 0)
		}
		if p.First {
			add("first", 0, 0, 1, p.Name, p.Presses)
		}
		if p.Last {
			add("last", 0, 0, -1, p.Name, p.Presses)
		}
	}
	for rule, groups := range out {
		for i := range groups {
			out[rule][i].Names = joinAnd(strings.Split(groups[i].Names, "\x00"))
		}
	}
	return out
}

func joinAnd(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// gameRow is one player's game result for games/<type>.md; fields a game doesn't use are zero.
type gameRow struct {
	Name, Tribe, TribeEmoji                string
	Points, Presses, StreakDays            int
	First, Last, Solved                    bool
	Place, Seconds, WrongChecks, Guesses   int
	HintPoints, CleanPoints                int // Island Scramble: before-hint and fewest-wrong-checks points
	Score, Cups, BallsLeft                 int // Inside Track: in-game score = cups + balls left once all three are in
	Moves                                  int // Free the Block: slides to free the idol (Solved says whether it got out)
	RankRule                               string
	RankPoints, VolumePoints, StreakPoints int
}

type tribeRow struct {
	Name, Emoji string
	Value       float64
}

func fnum(v any) float64 {
	f, _ := v.(float64)
	return f
}
