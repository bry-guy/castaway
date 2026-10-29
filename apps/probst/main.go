package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

func main() {
	if err := newCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newCommand() *cobra.Command {
	var server, actor, instance, guild, discordUser string
	var asJSON, yes bool
	root := &cobra.Command{Use: "probst", Short: "Castaway operator client", SilenceUsage: true, SilenceErrors: true}
	var token, discordBotToken string
	var aliases map[string]alias
	var defaultChannel string // the --instance alias's channel, used when CHANNEL is omitted
	// resolveChannel turns an alias name into its channel ID (filling --guild if unset); IDs pass through.
	resolveChannel := func(channel string) string {
		if channel == "" {
			return defaultChannel
		}
		if a, ok := aliases[channel]; ok && a.Channel != "" {
			if guild == "" {
				guild = a.Guild
			}
			return a.Channel
		}
		return channel
	}
	root.PersistentFlags().StringVar(&server, "server", "", "Castaway API URL (PROBST_API_URL)")
	root.PersistentFlags().StringVar(&actor, "actor", "", "Admin Discord ID asserted by the trusted service")
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		if !root.PersistentFlags().Changed("server") {
			server = envOrFile("PROBST_API_URL", cfg.APIURL)
		}
		if !root.PersistentFlags().Changed("actor") {
			actor = envOrFile("PROBST_DISCORD_USER_ID", cfg.DiscordUserID)
		}
		token = envOrFile("PROBST_TOKEN", cfg.Token)
		discordBotToken = envOrFile("CASTAWAY_DISCORD_BOT_TOKEN", cfg.DiscordBotToken)
		aliases = cfg.Aliases
		if a, ok := aliases[instance]; ok {
			if a.Instance == "" {
				return fmt.Errorf("alias %q has no instance", instance)
			}
			instance = a.Instance
			defaultChannel = a.Channel
			if guild == "" {
				guild = a.Guild
			}
		}
		return nil
	}
	root.PersistentFlags().StringVar(&instance, "instance", "", "Instance UUID or alias from the config file")
	root.PersistentFlags().StringVar(&guild, "guild", "", "Discord guild ID")
	root.PersistentFlags().StringVar(&discordUser, "discord-user", "", "Discord account to link")
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON")
	root.PersistentFlags().BoolVar(&yes, "yes", false, "Confirm announcement sending or destructive changes")
	var call apiCall = func(ctx context.Context, method, path string, body, out any) error {
		if token == "" {
			return fmt.Errorf("set PROBST_TOKEN through your credential provider")
		}
		parsed, err := url.Parse(server)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("set --server or PROBST_API_URL to an API URL")
		}
		local := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
		// The scheduled in-cluster job opts in explicitly to plain HTTP for its own server URL.
		allowed := local || (os.Getenv("PROBST_ALLOW_HTTP_SERVER") != "" && os.Getenv("PROBST_ALLOW_HTTP_SERVER") == server)
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && allowed) {
			return fmt.Errorf("HTTPS required except for loopback test servers and PROBST_ALLOW_HTTP_SERVER")
		}
		if strings.TrimSpace(actor) == "" {
			return fmt.Errorf("set --actor or PROBST_DISCORD_USER_ID; this is trusted-service delegation, not human login")
		}
		var reader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				return err
			}
			reader = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(server, "/")+path, reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Discord-User-ID", actor)
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("API request failed: %w", err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			var failure struct {
				Error string `json:"error"`
			}
			_ = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&failure)
			return fmt.Errorf("API HTTP %d: %s", response.StatusCode, failure.Error)
		}
		if out == nil {
			return nil
		}
		return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out)
	}
	request := func(cmd *cobra.Command, method, path string, body any) error {
		var result map[string]any
		if err := call(cmd.Context(), method, path, body, &result); err != nil {
			return err
		}
		return printResult(cmd.OutOrStdout(), result, asJSON)
	}
	instancePath := func() (string, error) {
		if instance == "" {
			return "", fmt.Errorf("--instance is required")
		}
		return "/instances/" + url.PathEscape(instance), nil
	}
	channelPath := func(channel string) (string, error) {
		channel = resolveChannel(channel)
		if guild == "" {
			return "", fmt.Errorf("--guild is required")
		}
		return "/discord/guilds/" + url.PathEscape(guild) + "/channels/" + url.PathEscape(channel), nil
	}
	add := func(parent *cobra.Command, use string, count int, run func(*cobra.Command, []string) error) {
		parent.AddCommand(&cobra.Command{Use: use, Args: cobra.ExactArgs(count), RunE: run})
	}
	root.AddCommand(newWeekCommand(call, &instance, &yes))
	root.AddCommand(newSeasonCommand(call, &instance, &guild, &yes))
	root.AddCommand(newTrackCommand(call, &instance, &yes))
	root.AddCommand(newDiscussionCommand(call, &instance, &yes))
	root.AddCommand(newGameAdminCommands(call, &yes)...)
	auth := &cobra.Command{Use: "auth"}
	root.AddCommand(auth)
	add(auth, "status", 0, func(c *cobra.Command, _ []string) error { return request(c, "GET", "/admin/session", nil) })
	instances := &cobra.Command{Use: "instance"}
	root.AddCommand(instances)
	add(instances, "list", 0, func(c *cobra.Command, _ []string) error { return request(c, "GET", "/instances", nil) })
	add(instances, "show INSTANCE", 1, func(c *cobra.Command, a []string) error {
		return request(c, "GET", "/instances/"+url.PathEscape(a[0]), nil)
	})
	add(instances, "bootstrap-admin INSTANCE", 1, func(c *cobra.Command, a []string) error {
		return request(c, "POST", "/instances/"+url.PathEscape(a[0])+"/admins/bootstrap", nil)
	})
	var name, contestantsFile string
	var season int
	create := &cobra.Command{Use: "create", Short: "Create a legacy-mode instance with its contestants", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if name == "" || season <= 0 || contestantsFile == "" {
			return fmt.Errorf("--name, --season and --contestants-file are required")
		}
		data, err := os.ReadFile(contestantsFile)
		if err != nil {
			return err
		}
		var contestants []string
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				contestants = append(contestants, line)
			}
		}
		if len(contestants) == 0 {
			return fmt.Errorf("%s lists no contestants", contestantsFile)
		}
		return request(c, "POST", "/instances", map[string]any{"name": name, "season": season, "contestants": contestants})
	}}
	create.Flags().StringVar(&name, "name", "", "Instance name")
	create.Flags().IntVar(&season, "season", 0, "Survivor season number")
	create.Flags().StringVar(&contestantsFile, "contestants-file", "", `File with one contestant per line, e.g. Danny "Kilby" Kilby`)
	instances.AddCommand(create)
	contestants := &cobra.Command{Use: "contestant"}
	root.AddCommand(contestants)
	add(contestants, "list", 0, func(c *cobra.Command, _ []string) error {
		p, e := instancePath()
		if e != nil {
			return e
		}
		return request(c, "GET", p+"/contestants", nil)
	})
	add(contestants, `rename CONTESTANT NEW_NAME`, 2, func(c *cobra.Command, a []string) error {
		p, e := instancePath()
		if e != nil {
			return e
		}
		return request(c, "PATCH", p+"/contestants/"+url.PathEscape(a[0]), map[string]string{"name": a[1]})
	})
	channels := &cobra.Command{Use: "channel"}
	root.AddCommand(channels)
	add(channels, "show CHANNEL", 1, func(c *cobra.Command, a []string) error {
		p, e := channelPath(a[0])
		if e != nil {
			return e
		}
		return request(c, "GET", p, nil)
	})
	add(channels, "bind CHANNEL", 1, func(c *cobra.Command, a []string) error {
		p, e := channelPath(a[0])
		if e != nil {
			return e
		}
		if instance == "" {
			return fmt.Errorf("--instance is required")
		}
		return request(c, "PUT", p, map[string]any{"instance_id": instance, "replace": yes})
	})
	add(channels, "unbind CHANNEL", 1, func(c *cobra.Command, a []string) error {
		if !yes {
			return fmt.Errorf("unbinding requires --yes")
		}
		p, e := channelPath(a[0])
		if e != nil {
			return e
		}
		return request(c, "DELETE", p, nil)
	})
	players := &cobra.Command{Use: "player"}
	root.AddCommand(players)
	add(players, "list", 0, func(c *cobra.Command, _ []string) error {
		p, e := instancePath()
		if e != nil {
			return e
		}
		return request(c, "GET", p+"/participants", nil)
	})
	add(players, "add NAME", 1, func(c *cobra.Command, a []string) error {
		p, e := instancePath()
		if e != nil {
			return e
		}
		var created struct {
			Participant participant `json:"participant"`
		}
		// Reuse a same-name player so a retry after a failed link does not create a duplicate.
		existing, err := loadParticipants(c.Context(), call, p)
		if err != nil {
			return err
		}
		if found, err := findParticipant(existing, a[0]); err == nil {
			created.Participant = *found
		} else if !strings.Contains(err.Error(), "matched 0") {
			return err
		} else if err := call(c.Context(), "POST", p+"/participants", map[string]string{"name": a[0]}, &created); err != nil {
			return err
		}
		if discordUser == "" {
			return printResult(c.OutOrStdout(), map[string]any{"participant": created.Participant}, asJSON)
		}
		return request(c, "PUT", p+"/participants/"+url.PathEscape(created.Participant.ID)+"/discord-link", map[string]string{"discord_user_id": discordUser})
	})
	add(players, "link PARTICIPANT", 1, func(c *cobra.Command, a []string) error {
		p, e := instancePath()
		if e != nil {
			return e
		}
		if discordUser == "" {
			return fmt.Errorf("--discord-user is required")
		}
		return request(c, "PUT", p+"/participants/"+url.PathEscape(a[0])+"/discord-link", map[string]string{"discord_user_id": discordUser})
	})
	add(players, "unlink PARTICIPANT", 1, func(c *cobra.Command, a []string) error {
		if !yes {
			return fmt.Errorf("unlinking requires --yes")
		}
		p, e := instancePath()
		if e != nil {
			return e
		}
		return request(c, "DELETE", p+"/participants/"+url.PathEscape(a[0])+"/discord-link", nil)
	})
	drafts := &cobra.Command{Use: "draft"}
	root.AddCommand(drafts)
	add(drafts, "show PARTICIPANT", 1, func(c *cobra.Command, a []string) error {
		p, e := instancePath()
		if e != nil {
			return e
		}
		return request(c, "GET", p+"/drafts/"+url.PathEscape(a[0]), nil)
	})
	var openTribes string
	openCmd := &cobra.Command{
		Use:   "open",
		Short: "Start draft events: 1st/2nd drafts earn +2/+1, every first draft gets a balanced random tribe and a pinged Jeff post",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			p, e := instancePath()
			if e != nil {
				return e
			}
			channel := resolveChannel("")
			if channel == "" || guild == "" || openTribes == "" {
				return fmt.Errorf("--tribes and an --instance alias with a channel (or --guild) are required")
			}
			if !yes {
				_, e = fmt.Fprintf(c.OutOrStdout(), "Dry run — would open draft events with tribes %s, posting in channel %s. Re-run with --yes.\n", openTribes, channel)
				return e
			}
			return request(c, "POST", p+"/draft-submissions", map[string]any{"tribes": strings.Split(openTribes, ","), "guild_id": guild, "channel_id": channel})
		},
	}
	openCmd.Flags().StringVar(&openTribes, "tribes", "", "Comma-separated starting tribes, e.g. Savu,Toka")
	drafts.AddCommand(openCmd)
	drafts.AddCommand(&cobra.Command{
		Use:   "close",
		Short: "Stop draft bonuses and post the last-player message; later drafts still get a tribe",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			p, e := instancePath()
			if e != nil {
				return e
			}
			if !yes {
				_, e = fmt.Fprintf(c.OutOrStdout(), "Dry run — would close draft events and post the last-draft message. Re-run with --yes.\n")
				return e
			}
			return request(c, "POST", p+"/draft-submissions/close", nil)
		},
	})
	drafts.AddCommand(&cobra.Command{
		Use:   "watch THREAD_URL",
		Short: "Have the bot read drafts posted in a thread: complete drafts save, problems DM the admin",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			p, e := instancePath()
			if e != nil {
				return e
			}
			thread, e := threadChannelID(a[0])
			if e != nil {
				return e
			}
			if !yes {
				_, e = fmt.Fprintf(c.OutOrStdout(), "Dry run — the bot would watch thread %s: new drafts there save (and post) automatically. Re-run with --yes.\n", thread)
				return e
			}
			return request(c, "PUT", p+"/draft-submissions/thread", map[string]string{"thread_id": thread})
		},
	})
	drafts.AddCommand(&cobra.Command{
		Use:   "reject PLAYER",
		Short: "Drop a player's incomplete draft submission; they lose their submission order",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			p, e := instancePath()
			if e != nil {
				return e
			}
			players, e := loadParticipants(c.Context(), call, p)
			if e != nil {
				return e
			}
			target, e := findParticipant(players, a[0])
			if e != nil {
				return e
			}
			if !yes {
				_, e = fmt.Fprintf(c.OutOrStdout(), "Dry run — would reject %s's pending draft; their next draft goes to the back of the line. Re-run with --yes.\n", target.Name)
				return e
			}
			return request(c, "POST", p+"/draft-submissions/"+url.PathEscape(target.ID)+"/reject", nil)
		},
	})
	var draftFile, draftParticipant, before string
	var verbose bool
	importCmd := &cobra.Command{
		Use:   "import [THREAD_URL]",
		Short: "Parse drafts from a Discord thread or a text file; dry run unless --yes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			p, e := instancePath()
			if e != nil {
				return e
			}
			if (len(a) == 1) == (draftFile != "") {
				return fmt.Errorf("give either a thread URL or --file")
			}
			ctx := c.Context()
			roster, err := loadRoster(ctx, call, p)
			if err != nil {
				return err
			}
			players, err := loadParticipants(ctx, call, p)
			if err != nil {
				return err
			}
			var rows []importRow
			if draftFile != "" {
				if draftParticipant == "" {
					return fmt.Errorf("--participant is required with --file")
				}
				target, err := findParticipant(players, draftParticipant)
				if err != nil {
					return err
				}
				data, err := os.ReadFile(draftFile)
				if err != nil {
					return err
				}
				d := parseDraft(string(data), roster)
				row := importRow{Player: target.Name, Author: draftFile, Status: "READY", draft: d, target: target}
				if !d.Ready() {
					row.Status, row.Detail = "NEEDS REVIEW", strings.Join(d.Problems, "; ")
				}
				rows, verbose = []importRow{row}, true
			} else {
				channelID, err := threadChannelID(a[0])
				if err != nil {
					return err
				}
				var cutoff *time.Time
				if before != "" {
					t, err := time.Parse(time.RFC3339, before)
					if err != nil {
						return fmt.Errorf("--before must be RFC3339, e.g. 2026-10-01T00:00:00Z: %w", err)
					}
					cutoff = &t
				}
				messages, err := fetchThread(ctx, channelID, discordBotToken)
				if err != nil {
					return err
				}
				rows = reviewThread(messages, roster, players, cutoff)
			}
			if err := submitReady(ctx, call, p, rows, yes); err != nil {
				return err
			}
			if err := printImport(c.OutOrStdout(), rows, verbose); err != nil {
				return err
			}
			if !yes {
				_, err = fmt.Fprintln(c.OutOrStdout(), "\nDry run: nothing was written. Re-run with --yes to submit READY drafts.")
			}
			return err
		},
	}
	importCmd.Flags().StringVar(&draftFile, "file", "", "Read one draft from a text file instead of Discord")
	importCmd.Flags().StringVar(&draftParticipant, "participant", "", "Player ID or exact name for --file")
	importCmd.Flags().StringVar(&before, "before", "", "Ignore thread drafts posted after this RFC3339 time")
	importCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show how every line was matched")
	drafts.AddCommand(importCmd)
	announcements := &cobra.Command{Use: "announcement"}
	root.AddCommand(announcements)
	var announcementFile, announcementKey, announcementAt string
	var notify bool
	send := &cobra.Command{Use: "send [CHANNEL]", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, a []string) error {
		a = []string{resolveChannel(strings.Join(a, ""))}
		if a[0] == "" {
			return fmt.Errorf("give a CHANNEL, or an --instance alias that has one")
		}
		p, err := instancePath()
		if err != nil {
			return err
		}
		bindingPath, err := channelPath(a[0])
		if err != nil {
			return err
		}
		if announcementFile == "" {
			return fmt.Errorf("--file is required")
		}
		data, err := os.ReadFile(announcementFile)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" || utf8.RuneCount(data) > 2000 {
			return fmt.Errorf("announcement must be nonempty UTF-8, at most 2000 characters")
		}
		var bound struct {
			Binding struct {
				InstanceID string `json:"instance_id"`
			} `json:"binding"`
		}
		if err := call(c.Context(), "GET", bindingPath, nil, &bound); err != nil {
			return err
		}
		if bound.Binding.InstanceID != instance {
			return fmt.Errorf("channel is not bound to --instance")
		}
		body := map[string]any{"guild_id": guild, "channel_id": a[0], "body": string(data), "notify_users": notify}
		when := "now"
		if announcementAt != "" {
			at, err := parseAnnouncementTime(announcementAt)
			if err != nil {
				return err
			}
			body["scheduled_at"] = at.UTC().Format(time.RFC3339)
			when = at.Format("Mon 2006-01-02 15:04 MST")
		}
		key := announcementKey
		if key == "" {
			hash := sha256.Sum256([]byte(instance + "\x00" + guild + "\x00" + a[0] + "\x00" + announcementAt + "\x00" + string(data)))
			key = hex.EncodeToString(hash[:16])
		}
		body["request_key"] = key
		if !yes {
			_, err = fmt.Fprintf(c.OutOrStdout(), "Dry run — instance %s, guild %s, channel %s, send %s, request key %s (%s):\n\n%s\n\nRe-run with --yes to enqueue.\n", instance, guild, a[0], when, key, mentionNote(notify), data)
			return err
		}
		if announcementAt == "" { // sending now is your own action: no approval
			return request(c, "POST", p+"/announcements", body)
		}
		body["draft"] = true // scheduled announcements wait for an admin's "yes"
		var saved struct {
			Announcement announcement `json:"announcement"`
		}
		if err := call(c.Context(), "POST", p+"/announcements", body, &saved); err != nil {
			return err
		}
		sendAt, _ := body["scheduled_at"].(string)
		if sendAt == "" {
			sendAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		}
		if err := call(c.Context(), "PUT", p+"/announcements/"+url.PathEscape(saved.Announcement.ID)+"/approval", map[string]any{"send_at": sendAt}, nil); err != nil {
			return err
		}
		_, err = fmt.Fprintf(c.OutOrStdout(), "%s is waiting for approval: every admin gets a DM, and it posts %s once one replies \"yes\".\n", key, when)
		return err
	}}
	send.Flags().StringVar(&announcementFile, "file", "", "UTF-8 Markdown file to send verbatim")
	send.Flags().StringVar(&announcementKey, "key", "", "Stable request key (defaults to a hash of target, time, and content)")
	send.Flags().BoolVar(&notify, "notify", false, "Let <@user> mentions ping those players (never @everyone or roles)")
	send.Flags().StringVar(&announcementAt, "at", "", `Send later: "2006-01-02 15:04" in America/New_York, or RFC3339`)
	announcements.AddCommand(send)
	addAnnouncementDraftCommands(announcements, call, instancePath, func(ctx context.Context, channel string) error {
		bindingPath, err := channelPath(channel)
		if err != nil {
			return err
		}
		var bound struct {
			Binding struct {
				InstanceID string `json:"instance_id"`
			} `json:"binding"`
		}
		if err := call(ctx, "GET", bindingPath, nil, &bound); err != nil {
			return err
		}
		if bound.Binding.InstanceID != instance {
			return fmt.Errorf("channel is not bound to --instance")
		}
		return nil
	}, &guild, &yes, resolveChannel)
	addMessageCommand(root, &discordBotToken, &yes, resolveChannel)
	addBotCommand(root, call, &instance, &discordBotToken)
	add(root, "scores", 0, func(c *cobra.Command, _ []string) error {
		p, e := instancePath()
		if e != nil {
			return e
		}
		var result map[string]any
		if err := call(c.Context(), "GET", p+"/leaderboard", nil, &result); err != nil {
			return err
		}
		// Players who haven't drafted are hidden; they appear once they submit.
		if rows, ok := result["leaderboard"].([]any); ok {
			kept := []any{}
			for _, row := range rows {
				if r, ok := row.(map[string]any); !ok || r["has_draft"] != false {
					kept = append(kept, row)
				}
			}
			result["leaderboard"] = kept
		}
		return printResult(c.OutOrStdout(), result, asJSON)
	})
	addSeasonCommands(root, call, instancePath, &guild, &yes, &discordBotToken)
	addEpisodeSync(root, call, instancePath, &yes)
	addEpisodeImportCommand(episodeCommand(root), seasonCommand(root), call, &yes)
	addRecapCommand(root, call, instancePath, &instance)
	addLoginCommands(root, &server)
	admins := &cobra.Command{Use: "admin", Short: "Instance admins (Discord users)"}
	root.AddCommand(admins)
	for _, cmd := range root.Commands() {
		if cmd.Name() == "scores" {
			addScoresGenerateCommand(cmd, call)
		}
	}
	add(admins, "add DISCORD_USER", 1, func(c *cobra.Command, a []string) error {
		p, e := instancePath()
		if e != nil {
			return e
		}
		return request(c, "POST", p+"/admins", map[string]string{"discord_user_id": a[0]})
	})
	access := &cobra.Command{Use: "access", Short: "Website access requests from unlinked Discord users"}
	root.AddCommand(access)
	add(access, "list", 0, func(c *cobra.Command, _ []string) error { return request(c, "GET", "/access-requests", nil) })
	add(access, "deny DISCORD_USER", 1, func(c *cobra.Command, a []string) error {
		if err := call(c.Context(), "DELETE", "/access-requests/"+url.PathEscape(a[0]), nil, nil); err != nil {
			return err
		}
		_, err := fmt.Fprintln(c.OutOrStdout(), "Denied; the request is removed.")
		return err
	})
	// approve = `player add NAME --discord-user USER` (reuses a same-name player); the request then drops off the list.
	add(access, "approve DISCORD_USER NAME", 2, func(c *cobra.Command, a []string) error {
		discordUser = a[0]
		cmd, _, err := root.Find([]string{"player", "add"})
		if err != nil {
			return err
		}
		return cmd.RunE(c, a[1:])
	})
	add(root, "logout", 0, func(c *cobra.Command, _ []string) error {
		if err := call(c.Context(), "POST", "/auth/logout", nil, nil); err != nil {
			return err
		}
		_, err := fmt.Fprintln(c.OutOrStdout(), "Session revoked. Run probst login to sign in again.")
		return err
	})
	return root
}

func printResult(out io.Writer, result map[string]any, asJSON bool) error {
	if !asJSON {
		for _, key := range []string{"instances", "participants", "leaderboard"} {
			rows, ok := result[key].([]any)
			if !ok {
				continue
			}
			table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			if key == "leaderboard" {
				if _, err := fmt.Fprintln(table, "PLAYER\tDRAFT\tBONUS\tTOTAL"); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(table, "ID\tNAME\tDISCORD USER"); err != nil {
					return err
				}
			}
			for _, value := range rows {
				row, ok := value.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid API row")
				}
				var err error
				if key == "leaderboard" {
					_, err = fmt.Fprintf(table, "%v\t%v\t%v\t%v\n", row["participant_name"], row["draft_points"], row["bonus_points"], row["total_points"])
				} else {
					user := row["discord_user_id"]
					if user == nil {
						user = ""
					}
					_, err = fmt.Fprintf(table, "%v\t%v\t%v\n", row["id"], row["name"], user)
				}
				if err != nil {
					return err
				}
			}
			return table.Flush()
		}
	}
	encoder := json.NewEncoder(out)
	if !asJSON {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(result)
}

// parseAnnouncementTime accepts RFC3339 or a wall-clock time in America/New_York.
func parseAnnouncementTime(value string) (time.Time, error) {
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at, nil
	}
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, err
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", value, eastern)
	if err != nil {
		return time.Time{}, fmt.Errorf(`--at must be "2006-01-02 15:04" (America/New_York) or RFC3339`)
	}
	return at, nil
}
