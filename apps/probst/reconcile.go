package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// `probst season reconcile` is the scheduled job: each run looks at the season file and the live season
// and does whatever is due, once. Every step is safe to repeat (the server dedupes), and steps run
// independently, so one failure doesn't stop the rest. It never sends anything: scores posts go to the
// admins for approval and alerts go to the admins' DMs.

type automationConfig struct {
	Enabled  bool   `yaml:"enabled"`
	FromWeek int    `yaml:"from_week"` // first week automation handles (results, scores posts)
	Guild    string `yaml:"guild"`     // the Discord server the season's channel is in
	NudgeAt  string `yaml:"nudge_at"`  // daily reminder time, ET "15:04" (default 10:00)
	// ApprovalLead is how long before a scores post's time its approval DM goes out (a Go duration, default 12h).
	ApprovalLead string `yaml:"approval_lead"`
}

// approvalLead is automation.approval_lead, defaulting to 12 hours.
func approvalLead(f seasonFile) (time.Duration, error) {
	if f.Automation.ApprovalLead == "" {
		return 12 * time.Hour, nil
	}
	d, err := time.ParseDuration(f.Automation.ApprovalLead)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("automation.approval_lead %q: want a positive duration like 12h", f.Automation.ApprovalLead)
	}
	return d, nil
}

type reconciler struct {
	ctx  context.Context
	call apiCall
	f    seasonFile
	airs []time.Time
	now  time.Time
	path string
	tmpl templateSet
	out  io.Writer
	open []string // this run's still-open issues, for the daily nudge
	// fetchPlan is fetchEpisodePlan; tests swap it.
	fetchPlan func(episode int) (episodePlan, error)
}

func (r *reconciler) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.out, format+"\n", args...)
}

// alert DMs the season's admins once per key.
// Every alert but an expired post is also an open issue for the daily nudge.
func (r *reconciler) alert(key, body string) error {
	return r.raise(key, body, nil)
}

// raise queues an admin alert; with an action, admins approve it by replying "yes" and the server runs it.
// alertMsg DMs admins the season's messages/<name>.md filled with data.
func (r *reconciler) alertMsg(key, name string, data any) error {
	text, err := r.tmpl.render("messages/"+name+".md", data)
	if err != nil {
		return err
	}
	return r.alert(key, text)
}

func (r *reconciler) raise(key, body string, action map[string]any) error {
	if !strings.HasSuffix(key, "-expired") {
		r.open = append(r.open, body)
	}
	var res struct {
		Created bool `json:"created"`
	}
	req := map[string]any{"body": body}
	if action != nil {
		req["action"] = action
	}
	if err := r.call(r.ctx, "PUT", r.path+"/admin-alerts/"+url.PathEscape(key), req, &res); err != nil {
		return err
	}
	if res.Created {
		r.logf("alerted admins (%s)", key)
	}
	return nil
}

func hash8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

func (r *reconciler) rules() importRules {
	rules := importRules{}
	if v, ok := r.f.Scoring.Tribe["from_episode"].(int); ok {
		rules.TribeFrom = v
	}
	if v, ok := r.f.Scoring.MergeEpisode.(int); ok {
		rules.MergeEpisode = v
	}
	return rules
}

func (r *reconciler) imports() (map[int][]int, error) {
	var res struct {
		Imports []struct {
			Episode   int   `json:"episode_number"`
			Positions []int `json:"boot_positions"`
		} `json:"imports"`
	}
	if err := r.call(r.ctx, "GET", r.path+"/episode-imports", nil, &res); err != nil {
		return nil, err
	}
	done := map[int][]int{}
	for _, i := range res.Imports {
		done[i.Episode] = i.Positions
	}
	return done, nil
}

// importResults imports each aired episode (from from_week on) once survivoR has it complete. Missing
// results past the file's due time, holds, and conflicts become one-time admin alerts.
func (r *reconciler) importResults(done map[int][]int) error {
	var errs []error
	for ep := max(1, r.f.Automation.FromWeek); ep <= len(r.airs); ep++ {
		if _, ok := done[ep]; ok || r.now.Before(r.airs[ep-1].Add(3*time.Hour)) {
			continue
		}
		plan, err := r.fetchPlan(ep)
		if err != nil {
			errs = append(errs, fmt.Errorf("episode %d: %w", ep, err))
			continue
		}
		r.logf("%s", plan.describe())
		key := fmt.Sprintf("s%d-ep%d", r.f.Season, ep)
		switch {
		case plan.Status != "ready":
			due, err := planTime(r.f.Weekly.Results.Due, r.airs[ep-1])
			if err == nil && r.now.After(due) {
				errs = append(errs, r.alertMsg(key+"-results-missing", "results-missing", map[string]any{"Episode": ep, "Why": plan.Why, "Week": ep, "Season": r.f.Season}))
			}
		case len(plan.Holds) > 0:
			errs = append(errs, r.alertMsg(key+"-holds-"+hash8(strings.Join(plan.Holds, "\n")), "holds", map[string]any{"Episode": ep, "Holds": plan.Holds, "Season": r.f.Season, "Week": ep}))
		case len(plan.Review) > 0:
			if err := r.propose(key+"-import", plan); err != nil {
				errs = append(errs, err)
			}
		default:
			if err := r.applyPlan(plan, false); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// propose DMs admins the exact import for an episode that needs judgment (like two people leaving);
// replying "yes" records it. If survivoR changes before then, the alert is replaced and the old yes refused.
func (r *reconciler) propose(key string, plan episodePlan) error {
	body, err := r.importRequest(plan, false)
	if err != nil {
		return err
	}
	type boot struct {
		Name, Result string
		Position     int
	}
	type win struct{ Tribes, Kind, Points, Winners string }
	var boots []boot
	for _, x := range plan.Boots {
		boots = append(boots, boot{x.Name, x.Result, x.Position})
	}
	var wins, champs []win
	for _, ch := range plan.Challenges {
		wins = append(wins, win{Tribes: strings.Join(ch.Tribes, ", "), Kind: ch.Kind, Points: map[string]string{"immunity": "+2", "reward": "+1"}[ch.Kind]})
	}
	for _, ch := range plan.Champions {
		var names []string
		for _, w := range ch.Winners {
			names = append(names, w.Name)
		}
		champs = append(champs, win{Winners: strings.Join(names, ", "), Kind: ch.Kind})
	}
	text, err := r.tmpl.render("messages/import-proposal.md", map[string]any{"Episode": plan.Episode, "Revision": shortRev(plan.Revision),
		"Review": plan.Review, "Boots": boots, "Challenges": wins, "Champions": champs, "Week": plan.Episode})
	if err != nil {
		return err
	}
	return r.raise(key, text, map[string]any{"kind": "episode_import", "payload": body})
}

// importRequest is the server import for a plan, with challenge times from the server's episode schedule
// (as `probst challenge` uses), so the same challenge entered either way is caught as a duplicate.
func (r *reconciler) importRequest(plan episodePlan, manual bool) (map[string]any, error) {
	episodes, err := loadEpisodes(r.ctx, r.call, r.path)
	if err != nil {
		return nil, err
	}
	ep, err := findEpisode(episodes, plan.Episode)
	if err != nil {
		return nil, err
	}
	return plan.importBody(ep.AirsAt, manual), nil
}

func (r *reconciler) applyPlan(plan episodePlan, manual bool) error {
	body, err := r.importRequest(plan, manual)
	if err != nil {
		return err
	}
	var res struct {
		Status string `json:"status"`
	}
	err = r.call(r.ctx, "POST", r.path+"/episode-imports", body, &res)
	if err != nil && strings.Contains(err.Error(), "409") {
		return r.alertMsg(fmt.Sprintf("s%d-ep%d-conflict-%s", r.f.Season, plan.Episode, hash8(err.Error())), "import-conflict", map[string]any{"Episode": plan.Episode, "Error": err.Error()})
	}
	if err == nil {
		r.logf("episode %d: %s", plan.Episode, res.Status)
	}
	return err
}

type liveGame struct {
	Type     string    `json:"type"`
	Episode  int       `json:"episode_number"`
	Scored   bool      `json:"scored"`
	OpensAt  time.Time `json:"opens_at"`
	CutoffAt time.Time `json:"cutoff_at"`
}

// draftScoresPosts drafts each due week's scores post for approval from approval_lead (12h) before its time,
// once episode results are in, and keeps it matching the standings until it's sent. Before the time, a game
// that isn't scored yet gets a placeholder for its results; the server never sends such a draft, and at the
// time itself an unscored game blocks it (waiting alert). Drafting stops 3 hours after the post's time.
func (r *reconciler) draftScoresPosts(done map[int][]int) error {
	lead, err := approvalLead(r.f)
	if err != nil {
		return err
	}
	var games struct {
		Games []liveGame `json:"games"`
	}
	if err := r.call(r.ctx, "GET", r.path+"/scheduled-games", nil, &games); err != nil {
		return err
	}
	var errs []error
	for week := max(1, r.f.Automation.FromWeek); week <= len(r.airs); week++ {
		at, err := weekPostTime(r.f, r.airs, week)
		if err != nil || r.now.Before(at.Add(-lead)) || r.f.Weeks[week].ScoresPost != nil && r.f.Weeks[week].ScoresPost.Sent {
			continue
		}
		key := scoresKey(r.f.Season, week)
		if r.now.After(at.Add(3 * time.Hour)) {
			if existing, err := findAnnouncement(r.ctx, r.call, r.path, key); err != nil || existing.Status != "sent" {
				errs = append(errs, r.alertMsg(key+"-expired", "scores-expired", map[string]any{"Week": week, "At": at.Format("Mon Jan 2 3:04pm")}))
			}
			continue
		}
		var waiting []string
		if _, ok := done[week]; !ok {
			waiting = append(waiting, fmt.Sprintf("Episode %d results", week))
		}
		want := weekGame(r.f, week) // a configured game must exist before its post is drafted
		found, scored := false, false
		for _, g := range games.Games {
			if g.Episode != week {
				continue
			}
			if g.Type == want {
				found, scored = true, g.Scored
			} else if !g.Scored {
				waiting = append(waiting, gameNames[g.Type]+" scoring")
			}
		}
		if want != "" && !found {
			waiting = append(waiting, gameNames[want]+" (not created)")
		}
		placeholder := want != "" && found && !scored
		if placeholder && !r.now.Before(at) {
			waiting = append(waiting, gameNames[want]+" scoring")
			placeholder = false
		}
		if len(waiting) > 0 {
			r.logf("week %d scores post waiting for %s", week, strings.Join(waiting, " and "))
			if r.now.After(at.Add(15 * time.Minute)) {
				errs = append(errs, r.alertMsg(key+"-waiting", "scores-waiting", map[string]any{"Week": week, "Waiting": strings.Join(waiting, " and "), "Until": shortTime(at.Add(3 * time.Hour))}))
			}
			continue
		}
		if err := r.draftWeek(week, at, key, done[week], placeholder); err != nil {
			errs = append(errs, fmt.Errorf("week %d scores post: %w", week, err))
		}
	}
	return errors.Join(errs...)
}

// draftWeek saves the week's scores post for approval. With placeholder, the game's results are a placeholder
// (awaiting_results): the server holds the post, so it can't send until a redraft with the results.
func (r *reconciler) draftWeek(week int, at time.Time, key string, bootPositions []int, placeholder bool) error {
	var snap struct {
		Fingerprint string     `json:"fingerprint"`
		Leaderboard []scoreRow `json:"leaderboard"`
		Frozen      bool       `json:"frozen"`
	}
	if err := r.call(r.ctx, "PUT", r.path+"/score-snapshots/"+strconv.Itoa(week), map[string]string{"request_key": key}, &snap); err != nil {
		return err
	}
	if snap.Frozen {
		return nil // already sent
	}
	var prev struct {
		Leaderboard []scoreRow `json:"leaderboard"`
	}
	if week > 1 {
		if err := r.call(r.ctx, "GET", r.path+"/score-snapshots/"+strconv.Itoa(week-1), nil, &prev); err != nil {
			if !strings.Contains(err.Error(), "404") {
				return err
			}
			before, err := weekPostTime(r.f, r.airs, week-1) // weeks posted by hand: the board as of that post
			if err != nil {
				return err
			}
			if err := r.call(r.ctx, "GET", r.path+"/leaderboard?at="+url.QueryEscape(before.UTC().Format(time.RFC3339)), nil, &prev); err != nil {
				return err
			}
		}
	}
	booted, err := r.bootNames(bootPositions)
	if err != nil {
		return err
	}
	next, err := nextGameLine(r.tmpl, r.f, r.airs, week)
	if err != nil {
		return err
	}
	p, err := buildScoresPost(r.tmpl, r.f.Season, week, snap.Leaderboard, prev.Leaderboard, booted, siteURL, next)
	if err != nil {
		return err
	}
	if placeholder {
		p.GameScore = gameScorePlaceholder(weekGame(r.f, week))
	} else if p.GameScore, err = gameScore(r.ctx, r.call, r.path, r.tmpl, r.f, week); err != nil {
		return err
	}
	text, err := renderScoresPost(r.tmpl, p)
	if err != nil {
		return err
	}
	var res struct {
		Action string `json:"action"`
	}
	// The server fills game_results into an admin-edited placeholder draft once the game is scored.
	var gameResults string
	if !placeholder {
		gameResults = p.GameScore
	}
	err = r.call(r.ctx, "PUT", r.path+"/score-posts/"+url.PathEscape(key), map[string]any{
		"guild_id": r.f.Automation.Guild, "channel_id": r.f.Channel, "body": text, "send_at": at.UTC().Format(time.RFC3339),
		"score_fingerprint": snap.Fingerprint, "notify_users": true, "awaiting_results": placeholder,
		"game_results": gameResults, "results_placeholder": gameScorePlaceholder(weekGame(r.f, week)),
	}, &res)
	if err != nil && strings.Contains(err.Error(), "wasn't written by automation") {
		r.logf("week %d: %s was saved by hand; leaving it alone", week, key)
		return nil
	}
	if err == nil && res.Action == "results-missing" {
		r.logf("week %d scores post: the edited draft has no results placeholder left; it stays held (admins alerted)", week)
	} else if err == nil && res.Action != "unchanged" {
		r.logf("week %d scores post %s; admins get the approval DM", week, res.Action)
	}
	return err
}

// draftReminders drafts each week's game-day reminder (reminder.md) for approval approval_lead (12h) before it
// sends at weekly.reminder.at, once the game is open, like scores posts, so the approval DM isn't days early.
// It never pings.
func (r *reconciler) draftReminders() error {
	if r.f.Weekly.Reminder.At == "" {
		return nil
	}
	lead, err := approvalLead(r.f)
	if err != nil {
		return err
	}
	var games struct {
		Games []liveGame `json:"games"`
	}
	if err := r.call(r.ctx, "GET", r.path+"/scheduled-games", nil, &games); err != nil {
		return err
	}
	var errs []error
	for _, g := range games.Games {
		if g.Episode < max(1, r.f.Automation.FromWeek) || g.Episode > len(r.airs) || g.Type != weekGame(r.f, g.Episode) || r.now.Before(g.OpensAt) {
			continue
		}
		at, err := planTime(r.f.Weekly.Reminder.At, r.airs[g.Episode-1])
		if err != nil {
			return fmt.Errorf("weekly.reminder.at: %w", err)
		}
		if r.now.Before(at.Add(-lead)) {
			continue // too early for the approval DM
		}
		if !at.Before(g.CutoffAt) || r.now.After(at.Add(3*time.Hour)) {
			continue // a reminder after the game closes would be wrong; past ones have expired
		}
		closes := g.CutoffAt.In(eastern())
		text, err := r.tmpl.render("reminder.md", map[string]any{"Week": g.Episode, "Name": gameNames[g.Type], "Site": siteURL,
			"Closes": shortTime(closes)})
		if err != nil {
			return err
		}
		key := fmt.Sprintf("s%d-week-%d-reminder", r.f.Season, g.Episode)
		var res struct {
			Action string `json:"action"`
		}
		err = r.call(r.ctx, "PUT", r.path+"/score-posts/"+url.PathEscape(key), map[string]any{
			"guild_id": r.f.Automation.Guild, "channel_id": r.f.Channel, "body": text + "\n", "send_at": at.UTC().Format(time.RFC3339), "notify_users": false,
		}, &res)
		if err == nil && res.Action != "unchanged" {
			r.logf("week %d reminder %s; admins get the approval DM", g.Episode, res.Action)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

const siteURL = "https://castaway.bry-guy.net"

// bootNames are this episode's boots (by place), spoiler-tagged.
func (r *reconciler) bootNames(positions []int) ([]string, error) {
	if len(positions) == 0 {
		return nil, nil
	}
	var outs struct {
		Outcomes []struct {
			Position int    `json:"position"`
			Name     string `json:"contestant_name"`
		} `json:"outcomes"`
	}
	if err := r.call(r.ctx, "GET", r.path+"/outcomes", nil, &outs); err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.IntSlice(positions)))
	var names []string
	for _, pos := range positions {
		for _, o := range outs.Outcomes {
			if o.Position == pos && o.Name != "" {
				names = append(names, fmt.Sprintf("||%-14s||", "  "+shortName(o.Name)))
			}
		}
	}
	return names, nil
}

func (r *reconciler) run() error {
	if !r.f.Automation.Enabled {
		r.logf("automation is off for season %d (automation.enabled in the season file)", r.f.Season)
		return r.queueDiscussions()
	}
	r.open = nil
	var errs []error
	did, err := checkNextGame(r.ctx, r.call, r.tmpl, r.f, r.airs, r.now)
	if err == nil {
		r.logf("%s", did)
		p, ok := strings.CutPrefix(did, "alerted admins: ")
		if !ok {
			p, ok = strings.CutPrefix(did, "already alerted: ")
		}
		if ok {
			line, lerr := r.tmpl.render("messages/game-not-ready.md", map[string]any{"Problem": p, "Opens": ""})
			r.open = append(r.open, line)
			err = lerr
		}
	}
	errs = append(errs, err)
	done, err := r.imports()
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	errs = append(errs, r.importResults(done))
	if done, err = r.imports(); err != nil { // pick up what was just imported
		return errors.Join(append(errs, err)...)
	}
	errs = append(errs, r.draftScoresPosts(done))
	errs = append(errs, r.draftReminders())
	errs = append(errs, r.queueDiscussions())
	errs = append(errs, r.nudge())
	return errors.Join(errs...)
}

// nudge sends one daily DM, from nudge_at ET, listing every issue still open on that run. Issues are
// re-found each run, so anything fixed by then drops out; nothing open, no DM.
func (r *reconciler) nudge() error {
	at := r.f.Automation.NudgeAt
	if at == "" {
		at = "10:00"
	}
	t, err := time.Parse("15:04", at)
	if err != nil {
		return fmt.Errorf("automation.nudge_at %q: want HH:MM", at)
	}
	et := eastern()
	now := r.now.In(et)
	due := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, et)
	if len(r.open) == 0 || now.Before(due) {
		return nil
	}
	issues := r.open
	r.open = nil // the nudge isn't itself an open issue
	return r.alertMsg(fmt.Sprintf("s%d-nudge-%s", r.f.Season, now.Format("2006-01-02")), "nudge", map[string]any{"Issues": issues})
}

func newReconciler(c *cobra.Command, call apiCall, file string) (*reconciler, error) {
	f, err := loadSeasonFile(file)
	if err != nil {
		return nil, err
	}
	tmpl, err := seasonTemplates(file, f)
	if err != nil {
		return nil, err
	}
	return newReconcilerFor(c, call, f, tmpl)
}

// newSyncedReconciler runs from the season config synced into the database (`probst season sync`).
func newSyncedReconciler(c *cobra.Command, call apiCall, instance string) (*reconciler, error) {
	cfg, err := getSeasonConfig(c.Context(), call, instance)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("no season config synced for instance %s; run `probst season sync FILE --yes`", instance)
	}
	f, err := parseSeason([]byte(cfg.SeasonYAML), "synced season config "+cfg.Revision)
	if err != nil {
		return nil, err
	}
	if f.Instance != instance {
		return nil, fmt.Errorf("synced config is for instance %s, not %s", f.Instance, instance)
	}
	tmpl, err := parseBundle(cfg.ScoresTemplate)
	if err != nil {
		return nil, err
	}
	r, err := newReconcilerFor(c, call, f, tmpl)
	if err == nil {
		r.logf("season config revision %s (synced %s)", cfg.Revision, cfg.UpdatedAt.In(eastern()).Format("Jan 2 3:04pm"))
	}
	return r, err
}

func newReconcilerFor(c *cobra.Command, call apiCall, f seasonFile, tmpl templateSet) (*reconciler, error) {
	now := time.Now()
	_, airs, err := expandSeason(f, now)
	if err != nil {
		return nil, err
	}
	r := &reconciler{ctx: c.Context(), call: call, f: f, airs: airs, now: now, tmpl: tmpl, out: c.OutOrStdout(), path: "/instances/" + url.PathEscape(f.Instance)}
	r.fetchPlan = func(ep int) (episodePlan, error) {
		return fetchEpisodePlan(r.ctx, call, r.path, f.Season, ep, r.rules())
	}
	return r, nil
}

func addSeasonReconcileCommand(season *cobra.Command, call apiCall, instance *string) {
	season.AddCommand(&cobra.Command{
		Use:   "reconcile [FILE]",
		Short: "Scheduled job: readiness alerts, survivoR results import, scores-post drafts for approval (never sends)",
		Long: "Without FILE it runs the season config synced into the database for --instance (what the scheduled job does);\n" +
			"with FILE it runs that file directly (local testing).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			var r *reconciler
			var err error
			if len(a) == 1 {
				r, err = newReconciler(c, call, a[0])
			} else if *instance == "" {
				return fmt.Errorf("--instance is required without FILE")
			} else {
				r, err = newSyncedReconciler(c, call, *instance)
			}
			if err != nil {
				return err
			}
			if err := r.run(); err != nil {
				return err
			}
			if len(a) == 0 { // the scheduled job: tell the server it ran (a stale heartbeat DMs admins)
				return call(c.Context(), "PUT", r.path+"/automation-heartbeat", nil, nil)
			}
			return nil
		},
	})
}

// `probst episode import N --file F`: the same import the scheduled job runs, by hand. Items that need
// judgment are imported too when run by hand (you're the one approving); holds still need --resolve-holds.
func addEpisodeImportCommand(episodeCmd, season *cobra.Command, call apiCall, yes *bool) {
	var file string
	var episode int
	var resolveHolds bool
	run := func(c *cobra.Command) error {
		r, err := newReconciler(c, call, file)
		if err != nil {
			return err
		}
		if episode < 1 || episode > len(r.airs) {
			return fmt.Errorf("episode must be 1-%d", len(r.airs))
		}
		plan, err := r.fetchPlan(episode)
		if err != nil {
			return err
		}
		r.logf("%s", plan.describe())
		switch {
		case plan.Status != "ready" && !resolveHolds:
			return fmt.Errorf("episode %d isn't complete in survivoR yet", episode)
		case len(plan.Holds) > 0 && !resolveHolds:
			return fmt.Errorf("episode %d has holds: record them by hand (`probst boot` / `probst challenge`), then re-run with --resolve-holds", episode)
		case !*yes:
			r.logf("Dry run — re-run with --yes to import.")
			return nil
		}
		return r.applyPlan(plan, resolveHolds)
	}
	cmd := &cobra.Command{
		Use:   "import N --file SEASON_FILE",
		Short: "Import an episode's results from survivoR all at once (dry run unless --yes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			n, err := strconv.Atoi(a[0])
			if err != nil {
				return fmt.Errorf("N must be an episode number")
			}
			episode = n
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			return run(c)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Season file, e.g. seasons/51.yaml")
	cmd.Flags().BoolVar(&resolveHolds, "resolve-holds", false, "Held items were recorded by hand: import the rest and mark the episode done")
	episodeCmd.AddCommand(cmd)

	alias := &cobra.Command{
		Use: "import FILE --episode N", Hidden: true, Deprecated: "use `probst episode import N --file FILE`", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			file = a[0]
			return run(c)
		},
	}
	alias.Flags().IntVar(&episode, "episode", 0, "Episode number")
	alias.Flags().BoolVar(&resolveHolds, "resolve-holds", false, "Held items were recorded by hand: import the rest and mark the episode done")
	season.AddCommand(alias)
}
