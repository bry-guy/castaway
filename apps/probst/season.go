package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type episode struct {
	Number int       `json:"episode_number"`
	Label  string    `json:"label"`
	AirsAt time.Time `json:"airs_at"`
}

type tribe struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Color   string        `json:"color,omitempty"`
	Emoji   string        `json:"emoji,omitempty"`
	Members []tribeMember `json:"members"`
}

type tribeMember struct {
	ParticipantID string `json:"participant_id"`
	Name          string `json:"name"`
}

func loadEpisodes(ctx context.Context, call apiCall, instancePath string) ([]episode, error) {
	var res struct {
		Episodes []episode `json:"episodes"`
	}
	return res.Episodes, call(ctx, "GET", instancePath, nil, &res)
}

func findEpisode(episodes []episode, number int) (episode, error) {
	for _, e := range episodes {
		if e.Number == number {
			return e, nil
		}
	}
	return episode{}, fmt.Errorf("episode %d is not in this instance's schedule", number)
}

func loadTribes(ctx context.Context, call apiCall, instancePath string, at time.Time) ([]tribe, error) {
	var res struct {
		Tribes []tribe `json:"tribes"`
	}
	return res.Tribes, call(ctx, "GET", instancePath+"/tribes?at="+url.QueryEscape(at.UTC().Format(time.RFC3339Nano)), nil, &res)
}

// parseTribesFile reads lines like "Savu: Adam, Kate, Mooney" or "Dina (pink): Adam, Keeling" and resolves names to players.
func parseTribesFile(data string, players []participant) ([]map[string]any, error) {
	var tribes []map[string]any
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, list, ok := strings.Cut(line, ":")
		name, color, hasColor := strings.Cut(head, "(")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("expected \"Tribe: Player, Player\" or \"Tribe (color): Player, Player\", got %q", line)
		}
		entry := map[string]any{"name": name}
		if hasColor {
			color, closed := strings.CutSuffix(strings.TrimSpace(color), ")")
			if color = strings.TrimSpace(color); !closed || color == "" {
				return nil, fmt.Errorf("expected a color like \"Dina (pink):\", got %q", line)
			}
			entry["color"] = color
		}
		var ids []string
		for _, ref := range strings.Split(list, ",") {
			if ref = strings.TrimSpace(ref); ref == "" {
				continue
			}
			p, err := findParticipant(players, ref)
			if err != nil {
				return nil, err
			}
			ids = append(ids, p.ID)
		}
		entry["participant_ids"] = ids
		tribes = append(tribes, entry)
	}
	if len(tribes) == 0 {
		return nil, fmt.Errorf("no tribes found")
	}
	return tribes, nil
}

func printTribes(out io.Writer, at time.Time, tribes []tribe) error {
	if _, err := fmt.Fprintf(out, "Tribes at %s:\n", at.In(eastern()).Format("Mon 2006-01-02 15:04 MST")); err != nil {
		return err
	}
	for _, t := range tribes {
		names := make([]string, 0, len(t.Members))
		for _, m := range t.Members {
			names = append(names, m.Name)
		}
		if _, err := fmt.Fprintf(out, "  %s (%d): %s\n", t.Name, len(names), strings.Join(names, ", ")); err != nil {
			return err
		}
	}
	return nil
}

var wordleShare = regexp.MustCompile(`(?i)\bWordle\s+[\d,.]+\s+([1-6X])/6`)

// parseWordle returns the guess count from a Wordle share; a failed puzzle (X/6) counts as 7.
func parseWordle(content string) (int, bool) {
	match := wordleShare.FindStringSubmatch(content)
	if match == nil {
		return 0, false
	}
	if strings.EqualFold(match[1], "x") {
		return 7, true
	}
	n, _ := strconv.Atoi(match[1])
	return n, true
}

type wordleRow struct {
	Player  string
	Tribe   string
	Guesses int
	Status  string
	target  *participant
	tribeID string
}

// reviewWordle takes each linked player's first Wordle share posted inside [opens, cutoff).
func reviewWordle(messages []discordMessage, players []participant, tribes []tribe, opens, cutoff time.Time) []wordleRow {
	byDiscord := map[string]*participant{}
	for i := range players {
		if players[i].DiscordUserID != "" {
			byDiscord[players[i].DiscordUserID] = &players[i]
		}
	}
	tribeOf := map[string]tribe{}
	for _, t := range tribes {
		for _, m := range t.Members {
			tribeOf[m.ParticipantID] = t
		}
	}
	seen := map[string]bool{}
	var rows []wordleRow
	for _, m := range messages {
		guesses, ok := parseWordle(m.Content)
		if !ok || m.Timestamp.Before(opens) || !m.Timestamp.Before(cutoff) || seen[m.Author.ID] {
			continue
		}
		seen[m.Author.ID] = true
		p := byDiscord[m.Author.ID]
		if p == nil {
			rows = append(rows, wordleRow{Player: "@" + m.Author.Username, Guesses: guesses, Status: "SKIP: Discord user is not a linked player"})
			continue
		}
		t, ok := tribeOf[p.ID]
		if !ok {
			rows = append(rows, wordleRow{Player: p.Name, Guesses: guesses, Status: "SKIP: player is not on a tribe"})
			continue
		}
		rows = append(rows, wordleRow{Player: p.Name, Tribe: t.Name, Guesses: guesses, Status: "READY", target: p, tribeID: t.ID})
	}
	return rows
}

var wordleLine = regexp.MustCompile(`^(.+?)\s*[:=,-]?\s+([1-6xX])(?:/6)?$`)

// reviewWordleFile reads "Player: 3" lines (X = failed; blank lines and # comments ignored).
func reviewWordleFile(data string, players []participant, tribes []tribe) []wordleRow {
	tribeOf := map[string]tribe{}
	for _, t := range tribes {
		for _, m := range t.Members {
			tribeOf[m.ParticipantID] = t
		}
	}
	seen := map[string]bool{}
	var rows []wordleRow
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		match := wordleLine.FindStringSubmatch(line)
		if match == nil {
			rows = append(rows, wordleRow{Player: line, Status: "SKIP: expected \"Player: 1-6 or X\""})
			continue
		}
		guesses, _ := parseWordle("Wordle 0 " + match[2] + "/6")
		p, err := findParticipant(players, match[1])
		switch {
		case err != nil:
			rows = append(rows, wordleRow{Player: match[1], Guesses: guesses, Status: "SKIP: " + err.Error()})
		case seen[p.ID]:
			rows = append(rows, wordleRow{Player: p.Name, Guesses: guesses, Status: "SKIP: player listed twice"})
		default:
			seen[p.ID] = true
			if t, ok := tribeOf[p.ID]; ok {
				rows = append(rows, wordleRow{Player: p.Name, Tribe: t.Name, Guesses: guesses, Status: "READY", target: p, tribeID: t.ID})
			} else {
				rows = append(rows, wordleRow{Player: p.Name, Guesses: guesses, Status: "SKIP: player is not on a tribe"})
			}
		}
	}
	return rows
}

// Tribe changes land a minute after an episode starts so the Wordle that closes at that start is scored
// with the previous week's tribes; challenges are recorded an hour in, after the tribe change.
const (
	tribeChangeOffset = time.Minute
	challengeOffset   = time.Hour
)

// The Wordle posts with the scores at 1pm on an episode's air day (8pm ET) and closes at noon on the
// next one, an hour before the next scores post.
const (
	wordleOpensBefore = 7 * time.Hour
	wordleCloseBefore = 8 * time.Hour
)

func eastern() *time.Location {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return location
}

func addSeasonCommands(root *cobra.Command, call apiCall, instancePath func() (string, error), guild *string, yes *bool, discordBotToken *string) {
	tribes := &cobra.Command{Use: "tribes", Short: "Show or change player tribes"}
	root.AddCommand(tribes)
	addTribeAnnounceCommand(tribes, call, guild, yes)

	var showAt string
	show := &cobra.Command{Use: "show", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		p, err := instancePath()
		if err != nil {
			return err
		}
		at := time.Now()
		if showAt != "" {
			if at, err = parseAnnouncementTime(showAt); err != nil {
				return err
			}
		}
		current, err := loadTribes(c.Context(), call, p, at)
		if err != nil {
			return err
		}
		return printTribes(c.OutOrStdout(), at, current)
	}}
	show.Flags().StringVar(&showAt, "at", "", `Show tribes at "2006-01-02 15:04" (America/New_York) or RFC3339; default now`)
	tribes.AddCommand(show)

	var tribesFile string
	var tribesEpisode int
	set := &cobra.Command{
		Use:   "set",
		Short: "Replace all tribes from the start of an episode (start, swap, merge, split); dry run unless --yes",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			p, err := instancePath()
			if err != nil {
				return err
			}
			if tribesFile == "" || tribesEpisode <= 0 {
				return fmt.Errorf("--file and --episode are required")
			}
			ctx := c.Context()
			episodes, err := loadEpisodes(ctx, call, p)
			if err != nil {
				return err
			}
			ep, err := findEpisode(episodes, tribesEpisode)
			if err != nil {
				return err
			}
			players, err := loadParticipants(ctx, call, p)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(tribesFile)
			if err != nil {
				return err
			}
			assignments, err := parseTribesFile(string(data), players)
			if err != nil {
				return err
			}
			out := c.OutOrStdout()
			if !*yes {
				if _, err := fmt.Fprintf(out, "Dry run — these tribes take effect at the start of %s (%s):\n", ep.Label, ep.AirsAt.In(eastern()).Format("Mon 2006-01-02 15:04 MST")); err != nil {
					return err
				}
				for _, a := range assignments {
					label := a["name"].(string)
					if color, ok := a["color"].(string); ok {
						label += " (" + color + ")"
					}
					if _, err := fmt.Fprintf(out, "  %s: %d players\n", label, len(a["participant_ids"].([]string))); err != nil {
						return err
					}
				}
				_, err = fmt.Fprintln(out, "Players left out are taken off their tribe. Re-run with --yes to save.")
				return err
			}
			var res struct {
				Tribes []tribe `json:"tribes"`
			}
			if err := call(ctx, "PUT", p+"/tribes", map[string]any{"effective_at": ep.AirsAt.Add(tribeChangeOffset).UTC().Format(time.RFC3339), "tribes": assignments}, &res); err != nil {
				return err
			}
			return printTribes(out, ep.AirsAt.Add(tribeChangeOffset), res.Tribes)
		},
	}
	set.Flags().StringVar(&tribesFile, "file", "", `Lines like "Savu: Adam, Kate, Mooney" or "Dina (pink): Adam, Keeling"; a new tribe needs a color`)
	set.Flags().IntVar(&tribesEpisode, "episode", 0, "Episode whose start the tribes take effect at")
	tribes.AddCommand(set)

	color := &cobra.Command{
		Use:   "color TRIBE COLOR",
		Short: "Set an existing tribe's color without re-running a swap; dry run unless --yes",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, a []string) error {
			p, err := instancePath()
			if err != nil {
				return err
			}
			out := c.OutOrStdout()
			if !*yes {
				_, err := fmt.Fprintf(out, "Dry run — %s becomes %s. Re-run with --yes to save.\n", a[0], a[1])
				return err
			}
			var res tribe
			if err := call(c.Context(), "PATCH", p+"/tribes/"+url.PathEscape(a[0]), map[string]any{"color": a[1]}, &res); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "%s is now %s %s\n", res.Name, res.Emoji, res.Color)
			return err
		},
	}
	tribes.AddCommand(color)

	var challengeEpisode int
	var challengeKey string
	challenge := &cobra.Command{
		Use:   "challenge immunity|reward TRIBE...",
		Short: "Award a winning tribe's players (+2 immunity, +1 reward); dry run unless --yes",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, a []string) error {
			p, err := instancePath()
			if err != nil {
				return err
			}
			kind := strings.ToLower(a[0])
			if (kind != "immunity" && kind != "reward") || challengeEpisode <= 0 {
				return fmt.Errorf("usage: probst challenge immunity|reward TRIBE... --episode N")
			}
			ctx := c.Context()
			episodes, err := loadEpisodes(ctx, call, p)
			if err != nil {
				return err
			}
			ep, err := findEpisode(episodes, challengeEpisode)
			if err != nil {
				return err
			}
			if merge, err := liveMergeEpisode(ctx, call, p); err != nil {
				return err
			} else if merge > 0 && ep.Number >= merge {
				return fmt.Errorf("episode %d is at or after the merge (episode %d): Tribe scoring has ended", ep.Number, merge)
			}
			key := challengeKey
			if key == "" {
				key = fmt.Sprintf("ep%d-%s", ep.Number, kind)
			}
			out := c.OutOrStdout()
			if !*yes {
				_, err = fmt.Fprintf(out, "Dry run — %s: %s won %s (key %s); players on those tribes during the episode get points. Re-run with --yes to award.\n", ep.Label, strings.Join(a[1:], ", "), kind, key)
				return err
			}
			var res struct {
				AwardedCount int `json:"awarded_count"`
			}
			body := map[string]any{"key": key, "kind": kind, "winning_tribes": a[1:], "effective_at": ep.AirsAt.Add(challengeOffset).UTC().Format(time.RFC3339)}
			if err := call(ctx, "POST", p+"/tribe-challenges", body, &res); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "%s %s: awarded %d players.\n", ep.Label, kind, res.AwardedCount)
			return err
		},
	}
	challenge.Flags().IntVar(&challengeEpisode, "episode", 0, "Episode the challenge happened in")
	challenge.Flags().StringVar(&challengeKey, "key", "", "Override the default ep<N>-<kind> key, e.g. for a second reward in one episode")
	root.AddCommand(challenge)

	root.AddCommand(&cobra.Command{
		Use:   "boot PLACE CONTESTANT",
		Short: "Record who finished at PLACE (21 = first out), e.g. a medevac the import held; dry run unless --yes",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, a []string) error {
			p, err := instancePath()
			if err != nil {
				return err
			}
			place, err := strconv.Atoi(a[0])
			if err != nil || place <= 0 {
				return fmt.Errorf("PLACE must be a positive number")
			}
			roster, err := loadRoster(c.Context(), call, p)
			if err != nil {
				return err
			}
			match, method, _ := matchName(normalize(a[1]), roster)
			if match == nil || method != "exact" {
				return fmt.Errorf("%q doesn't exactly match one contestant", a[1])
			}
			if !*yes {
				_, err = fmt.Fprintf(c.OutOrStdout(), "Dry run — would record %s at place %d. Re-run with --yes.\n", match.Name, place)
				return err
			}
			if err := call(c.Context(), "PUT", p+"/outcomes/"+strconv.Itoa(place), map[string]any{"contestant_id": match.ID, "reason": "probst boot"}, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(c.OutOrStdout(), "Recorded %s at place %d.\n", match.Name, place)
			return err
		},
	})

	wordle := &cobra.Command{Use: "wordle", Short: "Run the weekly Wordle: open, import, resolve"}
	root.AddCommand(wordle)
	var wordleEpisode int
	wordle.PersistentFlags().IntVar(&wordleEpisode, "episode", 0, "Wordle opens 1pm ET on this episode's air day and closes noon on the next episode's")

	// round creates (or, on repeat, returns) the Wordle round for --episode.
	round := func(ctx context.Context) (string, string, time.Time, time.Time, error) {
		p, err := instancePath()
		if err != nil {
			return "", "", time.Time{}, time.Time{}, err
		}
		episodes, err := loadEpisodes(ctx, call, p)
		if err != nil {
			return "", "", time.Time{}, time.Time{}, err
		}
		ep, err := findEpisode(episodes, wordleEpisode)
		if err != nil {
			return "", "", time.Time{}, time.Time{}, err
		}
		next, err := findEpisode(episodes, wordleEpisode+1)
		if err != nil {
			return "", "", time.Time{}, time.Time{}, fmt.Errorf("the Wordle closes on the next episode's air day: %w", err)
		}
		var activities struct {
			Activities []struct {
				ID           string `json:"id"`
				ActivityType string `json:"activity_type"`
			} `json:"activities"`
		}
		if err := call(ctx, "GET", p+"/activities", nil, &activities); err != nil {
			return "", "", time.Time{}, time.Time{}, err
		}
		activityID := ""
		for _, a := range activities.Activities {
			if a.ActivityType == "tribe_wordle" {
				activityID = a.ID
			}
		}
		if activityID == "" {
			var created struct {
				Activity struct {
					ID string `json:"id"`
				} `json:"activity"`
			}
			body := map[string]any{"activity_type": "tribe_wordle", "name": "Wordle", "status": "active", "starts_at": ep.AirsAt.UTC().Format(time.RFC3339), "metadata": map[string]string{"scoring": "individual_and_tribe_average"}}
			if err := call(ctx, "POST", p+"/activities", body, &created); err != nil {
				return "", "", time.Time{}, time.Time{}, err
			}
			activityID = created.Activity.ID
		}
		var res struct {
			Round struct {
				ID string `json:"id"`
			} `json:"round"`
		}
		opens, cutoff := ep.AirsAt.Add(-wordleOpensBefore), next.AirsAt.Add(-wordleCloseBefore)
		body := map[string]any{"round_key": fmt.Sprintf("ep%d", ep.Number), "name": ep.Label + " Wordle", "opens_at": opens.UTC().Format(time.RFC3339), "cutoff_at": cutoff.UTC().Format(time.RFC3339)}
		if err := call(ctx, "POST", "/activities/"+url.PathEscape(activityID)+"/wordle-rounds", body, &res); err != nil {
			return "", "", time.Time{}, time.Time{}, err
		}
		return p, res.Round.ID, opens, cutoff, nil
	}

	wordle.AddCommand(&cobra.Command{Use: "open", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		_, _, opens, cutoff, err := round(c.Context())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(c.OutOrStdout(), "Wordle open %s → %s.\n", opens.In(eastern()).Format("Mon 01/02 15:04"), cutoff.In(eastern()).Format("Mon 01/02 15:04 MST"))
		return err
	}})

	var wordleFile string
	wordleImport := &cobra.Command{
		Use:   "import [THREAD_URL]",
		Short: "Save results from --file or a Discord thread's Wordle shares; dry run unless --yes (must run before the cutoff)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			ctx := c.Context()
			if (len(a) == 1) == (wordleFile != "") {
				return fmt.Errorf("give either a THREAD_URL or --file")
			}
			p, roundID, opens, cutoff, err := round(ctx)
			if err != nil {
				return err
			}
			players, err := loadParticipants(ctx, call, p)
			if err != nil {
				return err
			}
			current, err := loadTribes(ctx, call, p, cutoff)
			if err != nil {
				return err
			}
			var rows []wordleRow
			if wordleFile != "" {
				data, err := os.ReadFile(wordleFile)
				if err != nil {
					return err
				}
				rows = reviewWordleFile(string(data), players, current)
			} else {
				channelID, err := threadChannelID(a[0])
				if err != nil {
					return err
				}
				messages, err := fetchThread(ctx, channelID, *discordBotToken)
				if err != nil {
					return err
				}
				rows = reviewWordle(messages, players, current, opens, cutoff)
			}
			if *yes {
				for i, row := range rows {
					if row.target == nil {
						continue
					}
					body := map[string]any{"participant_group_id": row.tribeID, "guess_count": row.Guesses}
					if err := call(ctx, "PUT", "/wordle-rounds/"+url.PathEscape(roundID)+"/participants/"+url.PathEscape(row.target.ID), body, nil); err != nil {
						rows[i].Status = "FAILED: " + err.Error()
					} else {
						rows[i].Status = "SAVED"
					}
				}
			}
			table := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(table, "PLAYER\tTRIBE\tGUESSES\tSTATUS"); err != nil {
				return err
			}
			for _, row := range rows {
				if _, err := fmt.Fprintf(table, "%s\t%s\t%d\t%s\n", row.Player, dash(row.Tribe), row.Guesses, row.Status); err != nil {
					return err
				}
			}
			if err := table.Flush(); err != nil {
				return err
			}
			if !*yes {
				_, err = fmt.Fprintln(c.OutOrStdout(), "\nDry run: nothing was saved. Re-run with --yes to save READY rows.")
			}
			return err
		},
	}
	wordleImport.Flags().StringVar(&wordleFile, "file", "", `Results file, one "Player: 3" per line (X = failed)`)
	wordle.AddCommand(wordleImport)

	wordle.AddCommand(&cobra.Command{Use: "submit PLAYER GUESSES", Short: "Save one player's result by hand (X = failed)", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, a []string) error {
		ctx := c.Context()
		guesses, ok := parseWordle("Wordle 0 " + a[1] + "/6")
		if !ok {
			return fmt.Errorf("GUESSES must be 1-6 or X")
		}
		p, roundID, _, cutoff, err := round(ctx)
		if err != nil {
			return err
		}
		players, err := loadParticipants(ctx, call, p)
		if err != nil {
			return err
		}
		target, err := findParticipant(players, a[0])
		if err != nil {
			return err
		}
		current, err := loadTribes(ctx, call, p, cutoff)
		if err != nil {
			return err
		}
		for _, t := range current {
			for _, m := range t.Members {
				if m.ParticipantID == target.ID {
					body := map[string]any{"participant_group_id": t.ID, "guess_count": guesses}
					if err := call(ctx, "PUT", "/wordle-rounds/"+url.PathEscape(roundID)+"/participants/"+url.PathEscape(target.ID), body, nil); err != nil {
						return err
					}
					_, err = fmt.Fprintf(c.OutOrStdout(), "Saved %s (%s): %d guesses.\n", target.Name, t.Name, guesses)
					return err
				}
			}
		}
		return fmt.Errorf("%s is not on a tribe when the Wordle closes", target.Name)
	}})

	wordle.AddCommand(&cobra.Command{Use: "resolve", Short: "Close the Wordle and award points (after the cutoff)", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		ctx := c.Context()
		_, roundID, _, _, err := round(ctx)
		if err != nil {
			return err
		}
		if !*yes {
			_, err = fmt.Fprintln(c.OutOrStdout(), "Dry run: re-run with --yes to close the Wordle and award points.")
			return err
		}
		if err := call(ctx, "POST", "/wordle-rounds/"+url.PathEscape(roundID)+"/close", nil, nil); err != nil {
			return err
		}
		var res struct {
			Entries []struct {
				Points int    `json:"points"`
				Reason string `json:"reason"`
			} `json:"created_entries"`
		}
		if err := call(ctx, "POST", "/wordle-rounds/"+url.PathEscape(roundID)+"/resolve", nil, &res); err != nil {
			return err
		}
		awards := map[string]int{}
		for _, e := range res.Entries {
			awards[fmt.Sprintf("%s (+%d)", e.Reason, e.Points)]++
		}
		for reason, count := range awards {
			if _, err := fmt.Fprintf(c.OutOrStdout(), "%s: %d players\n", reason, count); err != nil {
				return err
			}
		}
		return nil
	}})
}
