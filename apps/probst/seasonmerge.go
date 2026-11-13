package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var mergeLine = regexp.MustCompile(`(?m)^(\s*merge_episode:\s*)TBD\b`)

// championRoundBody is the Pick Your Champion window for merge episode n: it opens as the merge episode airs
// and locks when the next one airs; Champions score from then on.
func championRoundBody(f seasonFile, airs []time.Time) (map[string]any, string, error) {
	n, ok := f.Scoring.MergeEpisode.(int)
	if !ok {
		return nil, "", nil
	}
	if n < 1 || n >= len(airs) {
		return nil, "", fmt.Errorf("merge_episode %d needs a following episode (season has %d)", n, len(airs))
	}
	opens, locks := airs[n-1], airs[n]
	// scoring.champion.opens / .locks move the window, e.g. when the merge is set after its episode aired
	for key, at := range map[string]*time.Time{"opens": &opens, "locks": &locks} {
		if v, ok := f.Scoring.Champion[key].(string); ok {
			t, err := planTime(v, airs[n-1])
			if err != nil {
				return nil, "", fmt.Errorf("scoring.champion.%s: %w", key, err)
			}
			*at = t
		}
	}
	if !locks.After(opens) {
		return nil, "", fmt.Errorf("Pick Your Champion locks %s, before it opens", locks.Format(time.RFC3339))
	}
	line := fmt.Sprintf("Pick Your Champion (merge Episode %d): picks %s → lock %s ET", n, opens.Format("Mon Jan 02 3:04pm"), locks.Format("Mon Jan 02 3:04pm"))
	return map[string]any{"merge_episode": n, "opens_at": opens.UTC().Format(time.RFC3339), "locks_at": locks.UTC().Format(time.RFC3339)}, line, nil
}

// liveMergeEpisode is the instance's merge episode from its Pick Your Champion round, or 0 before the merge.
func liveMergeEpisode(ctx context.Context, call apiCall, instancePath string) (int, error) {
	var res struct {
		Round struct {
			MergeEpisode int `json:"merge_episode"`
		} `json:"round"`
	}
	if err := call(ctx, "GET", instancePath+"/champion-round", nil, &res); err != nil {
		if strings.Contains(err.Error(), "404") {
			return 0, nil
		}
		return 0, err
	}
	return res.Round.MergeEpisode, nil
}

func applyChampionRound(c *cobra.Command, call apiCall, f seasonFile, airs []time.Time, yes bool) error {
	body, line, err := championRoundBody(f, airs)
	if err != nil || body == nil {
		return err
	}
	if locks, _ := time.Parse(time.RFC3339, body["locks_at"].(string)); !time.Now().Before(locks) {
		fmt.Fprintln(c.OutOrStdout(), "locked (unchanged): "+line)
		return nil
	}
	if !yes {
		fmt.Fprintln(c.OutOrStdout(), "would apply "+line)
		return nil
	}
	if err := call(c.Context(), "PUT", "/instances/"+url.PathEscape(f.Instance)+"/champion-round", body, nil); err != nil {
		return fmt.Errorf("%s: %w", line, err)
	}
	fmt.Fprintln(c.OutOrStdout(), "applied "+line)
	return nil
}

// `probst season merge FILE N`: sets scoring.merge_episode in the file (Tribe scoring ends before Episode N),
// opens Pick Your Champion (locks at Episode N+1), and syncs the file for the scheduled job.
func addSeasonMergeCommand(season *cobra.Command, call apiCall, yes *bool) {
	season.AddCommand(&cobra.Command{
		Use:   "merge FILE N",
		Short: "Episode N is the merge: end Tribe scoring, open Pick Your Champion (locks at Episode N+1), sync (dry run unless --yes)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, a []string) error {
			n, err := strconv.Atoi(a[1])
			if err != nil {
				return fmt.Errorf("N must be an episode number")
			}
			raw, err := os.ReadFile(a[0])
			if err != nil {
				return err
			}
			if !mergeLine.Match(raw) {
				return fmt.Errorf("%s has no `merge_episode: TBD` line; the merge is already set", a[0])
			}
			edited := mergeLine.ReplaceAll(raw, []byte("${1}"+strconv.Itoa(n)))
			f, err := parseSeason(edited, a[0])
			if err != nil {
				return err
			}
			_, airs, err := expandSeason(f, time.Now())
			if err != nil {
				return err
			}
			if _, _, err := championRoundBody(f, airs); err != nil {
				return err
			}
			if !*yes {
				fmt.Fprintf(c.OutOrStdout(), "would set merge_episode: %d in %s\n", n, a[0])
				return applyChampionRound(c, call, f, airs, false)
			}
			if err := os.WriteFile(a[0], edited, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "set merge_episode: %d in %s (commit it)\n", n, a[0])
			if err := applyChampionRound(c, call, f, airs, true); err != nil {
				return err
			}
			return syncSeason(c, call, a[0], true)
		},
	})
}
