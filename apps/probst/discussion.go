package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"
)

// Episode discussion threads: each aired-or-airing episode's public thread is queued on the server for its air
// time (weekly.discussion_thread); the bot opens it and posts one line in the season channel. Queuing is
// idempotent by key, so repeated reconciles never open a second thread.

func discussionKey(season, episode int) string {
	return fmt.Sprintf("s%d-ep%d-discussion", season, episode)
}

func discussionName(season, episode int) string {
	return fmt.Sprintf("Survivor S%d E%d Discussion", season, episode)
}

// queueDiscussion queues (or confirms) one discussion thread; the server answers created, unchanged, or requeued.
func queueDiscussion(ctx context.Context, call apiCall, instancePath, guild, channel, name, key string, dueAt time.Time) (string, error) {
	var res struct {
		Action string `json:"action"`
	}
	err := call(ctx, "PUT", instancePath+"/discussion-threads/"+url.PathEscape(key), map[string]any{
		"guild_id": guild, "channel_id": channel, "name": name, "due_at": dueAt.UTC().Format(time.RFC3339),
	}, &res)
	return res.Action, err
}

// queueDiscussions queues each episode from its week (air time minus a day) through three hours after its air
// time, so a late reconcile still catches the window, but a feature switched on mid-season doesn't open old threads.
func (r *reconciler) queueDiscussions() error {
	if !r.f.Weekly.DiscussionThread {
		return nil
	}
	var errs []error
	for ep := max(1, r.f.Automation.FromWeek); ep <= len(r.airs); ep++ {
		at := r.airs[ep-1]
		if r.now.Before(at.Add(-24*time.Hour)) || r.now.After(at.Add(3*time.Hour)) {
			continue
		}
		key := discussionKey(r.f.Season, ep)
		action, err := queueDiscussion(r.ctx, r.call, r.path, r.f.Automation.Guild, r.f.Channel, discussionName(r.f.Season, ep), key, at)
		if err == nil && action != "unchanged" {
			r.logf("episode %d discussion thread %s (%s)", ep, action, key)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// newDiscussionCommand is `probst discussion open`: queue one episode's thread to open now (a dry run without --yes).
func newDiscussionCommand(call apiCall, instance *string, yes *bool) *cobra.Command {
	var season, episode int
	var guild, channel string
	discussion := &cobra.Command{Use: "discussion", Short: "Episode discussion threads", Args: cobra.NoArgs}
	open := &cobra.Command{
		Use:   "open",
		Short: "Open an episode's discussion thread now (test a season channel with this)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if season < 1 || episode < 1 || guild == "" || channel == "" || *instance == "" {
				return fmt.Errorf("--instance, --season, --episode, --guild and --channel are required")
			}
			name, key := discussionName(season, episode), discussionKey(season, episode)
			if !*yes {
				_, err := fmt.Fprintf(c.OutOrStdout(), "Dry run — would open %q in channel %s (guild %s, instance %s) now, key %s.\nRe-run with --yes to queue it; the bot posts it within seconds.\n", name, channel, guild, *instance, key)
				return err
			}
			action, err := queueDiscussion(c.Context(), call, "/instances/"+url.PathEscape(*instance), guild, channel, name, key, time.Now())
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(c.OutOrStdout(), "%s: %s (%s)\n", name, action, key)
			return err
		},
	}
	open.Flags().IntVar(&season, "season", 0, "Season number, e.g. 51")
	open.Flags().IntVar(&episode, "episode", 0, "Episode number")
	open.Flags().StringVar(&guild, "guild", "", "Discord guild ID of the season channel")
	open.Flags().StringVar(&channel, "channel", "", "Discord season channel ID")
	discussion.AddCommand(open)
	return discussion
}
