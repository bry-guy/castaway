package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// survivorFiles are the survivoR datasets episode sync keeps; each row carries version_season and episode.
var survivorFiles = []string{"castaways", "boot_order", "challenge_results", "challenge_description", "episodes", "vote_history", "tribe_colours", "advantage_movement", "journeys"}

type survivorEpisode struct {
	Season  int                         `json:"season"`
	Episode int                         `json:"episode"`
	Fetched time.Time                   `json:"fetched_at"`
	Tables  map[string][]map[string]any `json:"tables"`
}

// fetchSurvivorEpisode downloads survivoR's JSON and keeps the rows for one US season episode.
// Rows with no episode (e.g. tribe_colours) are kept for the whole season.
func fetchSurvivorEpisode(ctx context.Context, season, episode int) (survivorEpisode, error) {
	base := os.Getenv("PROBST_SURVIVOR_URL")
	if base == "" {
		base = "https://raw.githubusercontent.com/doehm/survivoR/master/dev/json"
	}
	return fetchSurvivorEpisodeFrom(ctx, base, season, episode)
}

func fetchSurvivorEpisodeFrom(ctx context.Context, base string, season, episode int) (survivorEpisode, error) {
	result := survivorEpisode{Season: season, Episode: episode, Fetched: time.Now().UTC(), Tables: map[string][]map[string]any{}}
	client := &http.Client{Timeout: 60 * time.Second}
	want := fmt.Sprintf("US%d", season)
	for _, name := range survivorFiles {
		req, err := http.NewRequestWithContext(ctx, "GET", base+"/"+name+".json", nil)
		if err != nil {
			return result, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return result, fmt.Errorf("download survivoR %s: %w", name, err)
		}
		var rows []map[string]any
		err = json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&rows)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			return result, fmt.Errorf("download survivoR %s: HTTP %d %v", name, resp.StatusCode, err)
		}
		kept := []map[string]any{}
		for _, row := range rows {
			if row["version_season"] != want {
				continue
			}
			if ep, ok := row["episode"].(float64); ok && int(ep) != episode {
				continue
			}
			kept = append(kept, row)
		}
		result.Tables[name] = kept
	}
	return result, nil
}

type bootRow struct {
	Name     string
	Place    int
	Result   string
	Match    *contestant
	Status   string
	Existing string
}

type challengeRow struct {
	ID       int
	Kind     string // Immunity, Reward, Immunity and Reward
	Outcome  string // Tribal, Individual, Team
	Winners  []string
	Awards   []string // immunity/reward kinds we score
	Name     string
	Existing bool
}

// bootsFromEpisode reads who left this episode and their final place (21 = first out).
func bootsFromEpisode(data survivorEpisode, roster []*contestant) []bootRow {
	var rows []bootRow
	for _, c := range data.Tables["castaways"] {
		place, _ := c["place"].(float64)
		if place == 0 {
			continue
		}
		name, _ := c["full_name"].(string)
		result, _ := c["result"].(string)
		row := bootRow{Name: name, Place: int(place), Result: result}
		if match, method, note := matchName(normalize(name), roster); match != nil && method == "exact" {
			row.Match, row.Status = match, "READY"
		} else {
			row.Status = strings.TrimSpace("SKIP: " + method + " " + note)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Place > rows[j].Place })
	return rows
}

// challengesFromEpisode groups winners per challenge; only tribal wins are scored (+2 immunity, +1 reward).
func challengesFromEpisode(data survivorEpisode) []challengeRow {
	byID := map[int]*challengeRow{}
	names := map[int]string{}
	for _, d := range data.Tables["challenge_description"] {
		id, _ := d["challenge_id"].(float64)
		names[int(id)], _ = d["name"].(string)
	}
	for _, r := range data.Tables["challenge_results"] {
		idf, _ := r["challenge_id"].(float64)
		id := int(idf)
		row := byID[id]
		if row == nil {
			kind, _ := r["challenge_type"].(string)
			outcome, _ := r["outcome_type"].(string)
			row = &challengeRow{ID: id, Kind: kind, Outcome: outcome, Name: names[id]}
			byID[id] = row
		}
		if won, _ := r["won"].(float64); won == 0 {
			continue
		}
		winner, _ := r["castaway"].(string)
		if row.Outcome == "Tribal" {
			winner, _ = r["tribe"].(string)
		}
		if !contains(row.Winners, winner) {
			row.Winners = append(row.Winners, winner)
		}
	}
	var rows []challengeRow
	for _, row := range byID {
		if row.Outcome == "Tribal" && len(row.Winners) > 0 {
			kind := strings.ToLower(row.Kind)
			if strings.Contains(kind, "immunity") {
				row.Awards = append(row.Awards, "immunity")
			}
			if strings.Contains(kind, "reward") {
				row.Awards = append(row.Awards, "reward")
			}
		}
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func addEpisodeSync(root *cobra.Command, call apiCall, instancePath func() (string, error), yes *bool) {
	episodeCmd := episodeCommand(root)
	var season, number int
	sync := &cobra.Command{
		Use:        "sync",
		Deprecated: "use `probst episode import N --file FILE`, which records everything at once or nothing",
		Short:      "Download survivoR's data for an episode, save it, and record boots and tribal challenge wins; dry run unless --yes",
		Args:       cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if season <= 0 || number <= 0 {
				return fmt.Errorf("--season and --episode are required")
			}
			p, err := instancePath()
			if err != nil {
				return err
			}
			ctx := c.Context()
			data, err := fetchSurvivorEpisode(ctx, season, number)
			if err != nil {
				return err
			}
			if len(data.Tables["challenge_results"]) == 0 && len(data.Tables["boot_order"]) == 0 {
				return fmt.Errorf("survivoR has no US%d episode %d data yet; try later or enter results by hand", season, number)
			}
			saved, err := saveSurvivorEpisode(data)
			if err != nil {
				return err
			}
			roster, err := loadRoster(ctx, call, p)
			if err != nil {
				return err
			}
			episodes, err := loadEpisodes(ctx, call, p)
			if err != nil {
				return err
			}
			ep, err := findEpisode(episodes, number)
			if err != nil {
				return err
			}
			var outcomes struct {
				Outcomes []struct {
					Position     int    `json:"position"`
					ContestantID string `json:"contestant_id"`
				} `json:"outcomes"`
			}
			if err := call(ctx, "GET", p+"/outcomes", nil, &outcomes); err != nil {
				return err
			}
			placed := map[int]string{}
			for _, o := range outcomes.Outcomes {
				placed[o.Position] = o.ContestantID
			}
			boots := bootsFromEpisode(data, roster)
			challenges := challengesFromEpisode(data)
			if merge, err := liveMergeEpisode(ctx, call, p); err != nil {
				return err
			} else if merge > 0 && number >= merge {
				challenges = nil // Tribe scoring ended at the merge; `episode import` scores Champions
			}
			out := c.OutOrStdout()
			if _, err := fmt.Fprintf(out, "survivoR US%d episode %d saved to %s\n\n", season, number, saved); err != nil {
				return err
			}
			table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(table, "BOOT\tPLACE\tRESULT\tSTATUS")
			for i, b := range boots {
				if b.Match != nil && placed[b.Place] == b.Match.ID {
					boots[i].Status = "ALREADY RECORDED"
				} else if b.Match != nil && placed[b.Place] != "" {
					boots[i].Status = "SKIP: place already holds someone else; fix by hand"
				}
				fmt.Fprintf(table, "%s\t%d\t%s\t%s\n", b.Name, b.Place, b.Result, boots[i].Status)
			}
			fmt.Fprintln(table, "\nCHALLENGE\tTYPE\tWINNERS\tPOINTS")
			for _, ch := range challenges {
				points := "none (not a tribal win)"
				if len(ch.Awards) > 0 {
					points = strings.Join(ch.Awards, " + ")
				}
				fmt.Fprintf(table, "%d %s\t%s %s\t%s\t%s\n", ch.ID, ch.Name, ch.Outcome, ch.Kind, strings.Join(ch.Winners, ", "), points)
			}
			if err := table.Flush(); err != nil {
				return err
			}
			if !*yes {
				_, err := fmt.Fprintln(out, "\nDry run: nothing was recorded. Re-run with --yes to record READY boots and tribal wins.")
				return err
			}
			for _, b := range boots {
				if b.Status != "READY" {
					continue
				}
				body := map[string]any{"contestant_id": b.Match.ID, "reason": fmt.Sprintf("survivoR US%d E%d", season, number)}
				if err := call(ctx, "PUT", p+"/outcomes/"+fmt.Sprint(b.Place), body, nil); err != nil {
					return fmt.Errorf("record %s at place %d: %w", b.Name, b.Place, err)
				}
				fmt.Fprintf(out, "Recorded %s at place %d.\n", b.Name, b.Place)
			}
			for _, ch := range challenges {
				for _, kind := range ch.Awards {
					key := fmt.Sprintf("survivor-ep%d-c%d-%s", number, ch.ID, kind)
					body := map[string]any{"key": key, "kind": kind, "winning_tribes": ch.Winners, "effective_at": ep.AirsAt.Add(challengeOffset).UTC().Format(time.RFC3339)}
					var res struct {
						AwardedCount int `json:"awarded_count"`
					}
					if err := call(ctx, "POST", p+"/tribe-challenges", body, &res); err != nil {
						return fmt.Errorf("record challenge %d %s: %w", ch.ID, kind, err)
					}
					fmt.Fprintf(out, "Challenge %d %s (%s): awarded %d players.\n", ch.ID, kind, strings.Join(ch.Winners, ", "), res.AwardedCount)
				}
			}
			return nil
		},
	}
	sync.Flags().IntVar(&season, "season", 0, "US season number, e.g. 51")
	sync.Flags().IntVar(&number, "episode", 0, "Episode number")
	episodeCmd.AddCommand(sync)
}

func saveSurvivorEpisode(data survivorEpisode) (string, error) {
	dir, err := probstDataDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "survivor", fmt.Sprintf("US%d", data.Season), fmt.Sprintf("episode-%02d.json", data.Episode))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, encoded, 0o644)
}

// episodeCommand returns the root's `episode` command, creating it once.
func episodeCommand(root *cobra.Command) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == "episode" {
			return c
		}
	}
	c := &cobra.Command{Use: "episode", Short: "Episode results from survivoR"}
	root.AddCommand(c)
	return c
}

// seasonCommand returns the root's `season` command.
func seasonCommand(root *cobra.Command) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == "season" {
			return c
		}
	}
	panic("season command not registered")
}
