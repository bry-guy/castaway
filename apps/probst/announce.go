package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

type announcement struct {
	ID          string     `json:"id"`
	ChannelID   string     `json:"channel_id"`
	RequestKey  string     `json:"request_key"`
	Body        string     `json:"body"`
	ScheduledAt *time.Time `json:"scheduled_at"`
	DueAt       time.Time  `json:"due_at"`
	Status      string     `json:"status"`
	MessageID   *string    `json:"message_id"`
	// Approval gate: a gated draft waits for an admin's "yes"; NotifiedAt is when its DM reached the admins.
	ApprovalGated      bool       `json:"approval_gated"`
	ApprovalSendAt     *time.Time `json:"approval_send_at"`
	ApprovalNotifiedAt *time.Time `json:"approval_notified_at"`
}

func readMessageFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" || utf8.RuneCount(data) > 2000 {
		return "", fmt.Errorf("message must be nonempty UTF-8, at most 2000 characters")
	}
	return string(data), nil
}

// findAnnouncement accepts an announcement's name (its request key) or ID.
func findAnnouncement(ctx context.Context, call apiCall, instancePath, ref string) (announcement, error) {
	var res struct {
		Announcements []announcement `json:"announcements"`
	}
	if err := call(ctx, "GET", instancePath+"/announcements", nil, &res); err != nil {
		return announcement{}, err
	}
	for _, a := range res.Announcements {
		if a.ID == ref || a.RequestKey == ref {
			return a, nil
		}
	}
	return announcement{}, fmt.Errorf("no announcement named %q; see `probst announcement list`", ref)
}

// addAnnouncementDraftCommands adds the save → edit → schedule workflow. The bot reads the text when it
// sends, so edits apply until the moment it goes out.
func addAnnouncementDraftCommands(parent *cobra.Command, call apiCall, instancePath func() (string, error), checkBinding func(ctx context.Context, channel string) error, guild *string, yes *bool, resolveChannel func(string) string) {
	out := func(c *cobra.Command, format string, args ...any) error {
		_, err := fmt.Fprintf(c.OutOrStdout(), format, args...)
		return err
	}
	var file string
	var notify bool
	save := &cobra.Command{Use: "save NAME [CHANNEL]", Short: "Save a draft announcement (not sent until scheduled)", Args: cobra.RangeArgs(1, 2), RunE: func(c *cobra.Command, a []string) error {
		a = []string{a[0], resolveChannel(strings.Join(a[1:], ""))}
		if a[1] == "" {
			return fmt.Errorf("give a CHANNEL, or an --instance alias that has one")
		}
		p, err := instancePath()
		if err != nil {
			return err
		}
		text, err := readMessageFile(file)
		if err != nil {
			return err
		}
		if err := checkBinding(c.Context(), a[1]); err != nil {
			return err
		}
		if !*yes {
			return out(c, "Dry run — draft %q for channel %s (%s):\n\n%s\n\nRe-run with --yes to save it.\n", a[0], a[1], mentionNote(notify), text)
		}
		body := map[string]any{"guild_id": *guild, "channel_id": a[1], "request_key": a[0], "body": text, "draft": true, "notify_users": notify}
		if err := call(c.Context(), "POST", p+"/announcements", body, nil); err != nil {
			return fmt.Errorf("%w (to change a saved draft, use `announcement edit`)", err)
		}
		return out(c, "Saved draft %q. Schedule it with `probst announcement schedule %s --at ...`.\n", a[0], a[0])
	}}
	save.Flags().StringVar(&file, "file", "", "UTF-8 Markdown file with the announcement text")
	save.Flags().BoolVar(&notify, "notify", false, "Let <@user> mentions ping those players (never @everyone or roles)")
	parent.AddCommand(save)

	edit := &cobra.Command{Use: "edit NAME", Short: "Replace an unsent announcement's text", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		p, err := instancePath()
		if err != nil {
			return err
		}
		text, err := readMessageFile(file)
		if err != nil {
			return err
		}
		found, err := findAnnouncement(c.Context(), call, p, a[0])
		if err != nil {
			return err
		}
		if !*yes {
			return out(c, "Dry run — %q (%s) will read:\n\n%s\n\nRe-run with --yes to save it.\n", a[0], found.Status, text)
		}
		if err := call(c.Context(), "PUT", p+"/announcements/"+url.PathEscape(found.ID)+"/body", map[string]string{"body": text}, nil); err != nil {
			return err
		}
		return out(c, "Updated %q.\n", a[0])
	}}
	edit.Flags().StringVar(&file, "file", "", "UTF-8 Markdown file with the new text")
	parent.AddCommand(edit)

	parent.AddCommand(&cobra.Command{Use: "show NAME", Short: "Print an announcement's current text", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		p, err := instancePath()
		if err != nil {
			return err
		}
		found, err := findAnnouncement(c.Context(), call, p, a[0])
		if err != nil {
			return err
		}
		return out(c, "%s — %s, channel %s\n\n%s\n", found.RequestKey, describeStatus(found), found.ChannelID, found.Body)
	}})

	var at string
	var approval bool
	schedule := &cobra.Command{Use: "schedule NAME", Aliases: []string{"reschedule"}, Short: "Request approval to send a saved announcement at --at (admins get a DM; it sends after a \"yes\")", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		p, err := instancePath()
		if err != nil {
			return err
		}
		if at == "" {
			return fmt.Errorf("give --at: every announcement waits for an admin's \"yes\", then sends at that time")
		}
		when, err := parseAnnouncementTime(at)
		if err != nil {
			return err
		}
		found, err := findAnnouncement(c.Context(), call, p, a[0])
		if err != nil {
			return err
		}
		if err := call(c.Context(), "PUT", p+"/announcements/"+url.PathEscape(found.ID)+"/approval", map[string]any{"send_at": when.UTC().Format(time.RFC3339)}, nil); err != nil {
			return err
		}
		return out(c, "%q is waiting for approval: every admin gets a DM, and it posts at %s once one replies \"yes\".\n", a[0], at)
	}}
	schedule.Flags().StringVar(&at, "at", "", `Send at "2006-01-02 15:04" America/New_York, or RFC3339`)
	schedule.Flags().BoolVar(&approval, "approval", true, "Always on: every announcement waits for approval")
	_ = schedule.Flags().MarkHidden("approval")
	parent.AddCommand(schedule)

	simple := func(use, short, method, suffix, done string, confirm bool) {
		parent.AddCommand(&cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
			if confirm && !*yes {
				return fmt.Errorf("%s requires --yes", strings.Fields(use)[0])
			}
			p, err := instancePath()
			if err != nil {
				return err
			}
			found, err := findAnnouncement(c.Context(), call, p, a[0])
			if err != nil {
				return err
			}
			if err := call(c.Context(), method, p+"/announcements/"+url.PathEscape(found.ID)+suffix, nil, nil); err != nil {
				return err
			}
			return out(c, "%q %s.\n", a[0], done)
		}})
	}
	simple("unschedule NAME", "Take an announcement off the schedule and keep it as a draft", "DELETE", "/schedule", "is a draft again", false)
	var sentMessage string
	markSent := &cobra.Command{Use: "mark-sent NAME", Short: "Record that you posted an announcement yourself, so the bot never sends it", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		p, err := instancePath()
		if err != nil {
			return err
		}
		found, err := findAnnouncement(c.Context(), call, p, a[0])
		if err != nil {
			return err
		}
		if err := call(c.Context(), "POST", p+"/announcements/"+url.PathEscape(found.ID)+"/sent", map[string]string{"message_id": sentMessage}, nil); err != nil {
			return err
		}
		return out(c, "%q marked sent.\n", a[0])
	}}
	markSent.Flags().StringVar(&sentMessage, "message", "", "Discord message ID of the post, if you have it")
	parent.AddCommand(markSent)
	simple("delete NAME", "Delete an unsent announcement", "DELETE", "", "deleted", true)

	parent.AddCommand(&cobra.Command{Use: "list", Short: "List this instance's announcements", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		p, err := instancePath()
		if err != nil {
			return err
		}
		var res struct {
			Announcements []announcement `json:"announcements"`
		}
		if err := call(c.Context(), "GET", p+"/announcements", nil, &res); err != nil {
			return err
		}
		table := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(table, "NAME\tSTATUS\tCHANNEL\tSTART OF TEXT"); err != nil {
			return err
		}
		for _, a := range res.Announcements {
			preview := strings.Join(strings.Fields(a.Body), " ")
			if r := []rune(preview); len(r) > 40 {
				preview = string(r[:40]) + "…"
			}
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", a.RequestKey, describeStatus(a), a.ChannelID, preview); err != nil {
				return err
			}
		}
		return table.Flush()
	}})
}

func describeStatus(a announcement) string {
	if a.ApprovalGated && a.Status == "draft" {
		switch {
		case a.ApprovalNotifiedAt != nil:
			return "awaiting approval · DM sent " + eastTime(*a.ApprovalNotifiedAt)
		case a.ApprovalSendAt != nil:
			return "approval DM pending (posts " + eastTime(*a.ApprovalSendAt) + ")"
		}
		return "approval DM pending"
	}
	switch a.Status {
	case "pending":
		if a.ApprovalGated {
			return "approved · scheduled " + eastTime(a.DueAt)
		}
		return "scheduled " + a.DueAt.In(eastern()).Format("Mon 01/02 15:04 MST")
	case "sent":
		if a.MessageID != nil {
			return "sent (message " + *a.MessageID + ")"
		}
	}
	return a.Status
}

// eastTime is "Oct 28 8:00am EDT".
func eastTime(t time.Time) string { return t.In(eastern()).Format("Jan 2 3:04pm MST") }

func addMessageCommand(root *cobra.Command, token *string, yes *bool, resolveChannel func(string) string) {
	var file, replyTo string
	var notify, silent bool
	message := &cobra.Command{
		Use:   "message [CHANNEL] [TEXT]",
		Short: "Post a one-off message as the bot right now (not saved); dry run unless --yes. CHANNEL defaults to the --instance alias's channel",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(c *cobra.Command, a []string) error {
			var channel, text string
			switch {
			case len(a) == 2 && file == "":
				channel, text = a[0], a[1]
			case len(a) == 1 && file == "":
				text = a[0]
			case len(a) <= 1 && file != "":
				channel = strings.Join(a, "")
				var err error
				if text, err = readMessageFile(file); err != nil {
					return err
				}
			default:
				return fmt.Errorf("give the TEXT or --file, not both")
			}
			channel = resolveChannel(channel)
			if channel == "" {
				return fmt.Errorf("give a CHANNEL, or an --instance alias that has one")
			}
			a = []string{channel}
			if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 2000 {
				return fmt.Errorf("message must be nonblank and at most 2000 characters")
			}
			if !*yes {
				_, err := fmt.Fprintf(c.OutOrStdout(), "Dry run — channel %s (%s):\n\n%s\n\nRe-run with --yes to post it.\n", a[0], mentionNote(notify), text)
				return err
			}
			id, err := sendDiscordMessage(c.Context(), *token, a[0], text, replyTo, notify, silent)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(c.OutOrStdout(), "Posted message %s.\n", id)
			return err
		},
	}
	message.Flags().StringVar(&file, "file", "", "UTF-8 Markdown file to post")
	message.Flags().BoolVar(&notify, "notify", false, "Let <@user> mentions ping those players (never @everyone or roles)")
	message.Flags().BoolVar(&silent, "silent", false, "Post without push notifications (like @silent)")
	message.Flags().StringVar(&replyTo, "reply-to", "", "Discord message ID to reply to")
	root.AddCommand(message)
}
// sendDiscordMessage posts text as the bot straight to Discord, without saving it. Mentions render but never notify.
func mentionNote(notify bool) string {
	if notify {
		return "<@user> mentions WILL ping those players"
	}
	return "mentions will not notify"
}

func sendDiscordMessage(ctx context.Context, token, channelID, text, replyTo string, notify, silent bool) (string, error) {
	if token == "" {
		return "", fmt.Errorf("set CASTAWAY_DISCORD_BOT_TOKEN (or discord_bot_token in the config file) to post to Discord")
	}
	base := os.Getenv("PROBST_DISCORD_API_URL")
	if base == "" {
		base = "https://discord.com/api/v10"
	}
	parse := []string{}
	if notify {
		parse = []string{"users"}
	}
	payload := map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": parse}}
	if silent {
		payload["flags"] = 1 << 12 // SUPPRESS_NOTIFICATIONS, same as @silent
	}
	if replyTo != "" {
		payload["message_reference"] = map[string]any{"message_id": replyTo, "fail_if_not_exists": false}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/channels/"+url.PathEscape(channelID)+"/messages", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		// Discord may have posted it anyway; say so instead of retrying.
		return "", fmt.Errorf("Discord request failed (check the channel before resending): %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Discord HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var sent struct {
		ID string `json:"id"`
	}
	return sent.ID, json.Unmarshal(body, &sent)
}

