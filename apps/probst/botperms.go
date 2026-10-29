package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Discord permission bits (https://discord.com/developers/docs/topics/permissions).
const (
	permAdministrator         int64 = 1 << 3
	permViewChannel           int64 = 1 << 10
	permSendMessages          int64 = 1 << 11
	permManageMessages        int64 = 1 << 13
	permReadMessageHistory    int64 = 1 << 16
	permCreatePublicThreads   int64 = 1 << 35
	permSendMessagesInThreads int64 = 1 << 38
)

// botNeededPerms are the permissions the bot needs in every season channel.
var botNeededPerms = []struct {
	bit  int64
	name string
}{
	{permViewChannel, "View Channel"},
	{permSendMessages, "Send Messages"},
	{permReadMessageHistory, "Read Message History"},
	{permManageMessages, "Manage Messages"},
	{permCreatePublicThreads, "Create Public Threads"},
	{permSendMessagesInThreads, "Send Messages in Threads"},
}

type permRole struct {
	ID, Name string
	Perms    int64
}

// permOverwrite is a channel overwrite for a role (Member false) or for one member (Member true).
type permOverwrite struct {
	ID          string
	Member      bool
	Allow, Deny int64
}

// permState is everything Discord needs to work out the bot's effective permissions in one channel.
type permState struct {
	GuildID, OwnerID, BotID string
	Roles                   []permRole // every guild role; @everyone has ID == GuildID
	MemberRoles             []string   // role IDs the bot holds
	Overwrites              []permOverwrite
}

func (s permState) held() []permRole {
	var out []permRole
	for _, r := range s.Roles {
		if r.ID == s.GuildID || slices.Contains(s.MemberRoles, r.ID) {
			out = append(out, r)
		}
	}
	return out
}

// applies reports whether an overwrite reaches the bot: @everyone, a role it holds, or the bot itself.
func (s permState) applies(o permOverwrite) bool {
	if o.Member {
		return o.ID == s.BotID
	}
	return o.ID == s.GuildID || slices.Contains(s.MemberRoles, o.ID)
}

// effectivePerms follows Discord's order: the owner and Administrator get everything; otherwise the channel's
// @everyone overwrite, then all role overwrites together, then the bot's member overwrite.
func effectivePerms(s permState) int64 {
	if s.BotID == s.OwnerID {
		return -1
	}
	var base int64
	for _, r := range s.held() {
		base |= r.Perms
	}
	if base&permAdministrator != 0 {
		return -1
	}
	perms := base
	for _, o := range s.Overwrites {
		if !o.Member && o.ID == s.GuildID {
			perms = (perms &^ o.Deny) | o.Allow
		}
	}
	var allow, deny int64
	for _, o := range s.Overwrites {
		if !o.Member && o.ID != s.GuildID && s.applies(o) {
			allow |= o.Allow
			deny |= o.Deny
		}
	}
	perms = (perms &^ deny) | allow
	for _, o := range s.Overwrites {
		if o.Member && o.ID == s.BotID {
			perms = (perms &^ o.Deny) | o.Allow
		}
	}
	return perms
}

func (s permState) overwriteLabel(o permOverwrite) string {
	if o.Member {
		return "the bot's member overwrite"
	}
	for _, r := range s.Roles {
		if r.ID == o.ID {
			return r.Name
		}
	}
	return o.ID
}

// permSources names the roles and overwrites behind one bit. It explains the verdict; effectivePerms decides it.
func permSources(s permState, bit int64) (grants, denies []string) {
	if s.BotID == s.OwnerID {
		return []string{"server owner"}, nil
	}
	for _, r := range s.held() {
		switch {
		case r.Perms&permAdministrator != 0:
			grants = append(grants, r.Name+" (Administrator)")
		case r.Perms&bit != 0:
			grants = append(grants, r.Name)
		}
	}
	for _, o := range s.Overwrites {
		if !s.applies(o) {
			continue
		}
		if o.Allow&bit != 0 {
			grants = append(grants, "overwrite "+s.overwriteLabel(o))
		}
		if o.Deny&bit != 0 {
			denies = append(denies, "overwrite "+s.overwriteLabel(o))
		}
	}
	return grants, denies
}

// reportBotChannel prints one channel's verdict per needed permission and returns the names still missing.
func reportBotChannel(w io.Writer, guild, channel, channelID string, s permState) []string {
	perms := effectivePerms(s)
	var missing []string
	fmt.Fprintf(w, "%s — #%s (%s)\n", guild, channel, channelID)
	for _, need := range botNeededPerms {
		grants, denies := permSources(s, need.bit)
		if perms&need.bit != 0 {
			fmt.Fprintf(w, "  ✓ %s — granted by %s\n", need.name, strings.Join(grants, ", "))
			continue
		}
		missing = append(missing, need.name)
		line := "  ✗ " + need.name
		if len(denies) > 0 {
			line += " — denied by " + strings.Join(denies, ", ")
		}
		fmt.Fprintln(w, line)
	}
	if len(missing) == 0 {
		fmt.Fprint(w, "  missing: none\n\n")
	} else {
		fmt.Fprintf(w, "  missing: %s\n\n", strings.Join(missing, ", "))
	}
	return missing
}

// discordGet reads one Discord REST resource as the bot.
func discordGet(ctx context.Context, token, path string, out any) error {
	base := os.Getenv("PROBST_DISCORD_API_URL")
	if base == "" {
		base = "https://discord.com/api/v10"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+token)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("Discord request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Discord HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func parsePerms(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

type botBinding struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
}

type botGuild struct {
	Name, OwnerID string
	Roles         []permRole
	MemberRoles   []string
}

func loadBotGuild(ctx context.Context, token, guildID, botID string) (*botGuild, error) {
	var g struct {
		Name    string `json:"name"`
		OwnerID string `json:"owner_id"`
	}
	if err := discordGet(ctx, token, "/guilds/"+url.PathEscape(guildID), &g); err != nil {
		return nil, err
	}
	var roles []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Permissions string `json:"permissions"`
	}
	if err := discordGet(ctx, token, "/guilds/"+url.PathEscape(guildID)+"/roles", &roles); err != nil {
		return nil, err
	}
	var member struct {
		Roles []string `json:"roles"`
	}
	if err := discordGet(ctx, token, "/guilds/"+url.PathEscape(guildID)+"/members/"+url.PathEscape(botID), &member); err != nil {
		return nil, fmt.Errorf("the bot isn't in this guild: %w", err)
	}
	out := &botGuild{Name: g.Name, OwnerID: g.OwnerID, MemberRoles: member.Roles}
	for _, r := range roles {
		perms, err := parsePerms(r.Permissions)
		if err != nil {
			return nil, fmt.Errorf("role %s permissions: %w", r.ID, err)
		}
		out.Roles = append(out.Roles, permRole{ID: r.ID, Name: r.Name, Perms: perms})
	}
	return out, nil
}

// checkBotPermissions reports the bot's effective permissions in each binding's channel. A channel that can't be
// read is reported and the rest still checked; the returned error names every failure.
func checkBotPermissions(ctx context.Context, w io.Writer, token string, bindings []botBinding) error {
	var me struct {
		ID string `json:"id"`
	}
	if err := discordGet(ctx, token, "/users/@me", &me); err != nil {
		return fmt.Errorf("read the bot's identity: %w", err)
	}
	guilds := map[string]*botGuild{}
	var errs []error
	for _, b := range bindings {
		g, ok := guilds[b.GuildID]
		if !ok {
			var err error
			if g, err = loadBotGuild(ctx, token, b.GuildID, me.ID); err != nil {
				errs = append(errs, fmt.Errorf("guild %s: %w", b.GuildID, err))
				fmt.Fprintf(w, "guild %s — could not check: %v\n\n", b.GuildID, err)
				continue
			}
			guilds[b.GuildID] = g
		}
		var ch struct {
			Name       string `json:"name"`
			GuildID    string `json:"guild_id"`
			Overwrites []struct {
				ID    string `json:"id"`
				Type  int    `json:"type"`
				Allow string `json:"allow"`
				Deny  string `json:"deny"`
			} `json:"permission_overwrites"`
		}
		if err := discordGet(ctx, token, "/channels/"+url.PathEscape(b.ChannelID), &ch); err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", b.ChannelID, err))
			fmt.Fprintf(w, "%s — could not check channel %s: %v\n\n", g.Name, b.ChannelID, err)
			continue
		}
		if ch.GuildID != b.GuildID {
			errs = append(errs, fmt.Errorf("channel %s is in guild %s, not %s", b.ChannelID, ch.GuildID, b.GuildID))
			fmt.Fprintf(w, "%s — channel %s is not in this guild\n\n", g.Name, b.ChannelID)
			continue
		}
		state := permState{GuildID: b.GuildID, OwnerID: g.OwnerID, BotID: me.ID, Roles: g.Roles, MemberRoles: g.MemberRoles}
		overwriteErr := error(nil)
		for _, o := range ch.Overwrites {
			allow, errA := parsePerms(o.Allow)
			deny, errD := parsePerms(o.Deny)
			overwriteErr = errors.Join(overwriteErr, errA, errD)
			state.Overwrites = append(state.Overwrites, permOverwrite{ID: o.ID, Member: o.Type == 1, Allow: allow, Deny: deny})
		}
		if overwriteErr != nil {
			errs = append(errs, fmt.Errorf("channel %s overwrites: %w", b.ChannelID, overwriteErr))
			continue
		}
		reportBotChannel(w, g.Name, ch.Name, b.ChannelID, state)
	}
	return errors.Join(errs...)
}

// addBotCommand is `probst bot permissions`: the bot's effective permissions in each channel bound to --instance.
func addBotCommand(root *cobra.Command, call apiCall, instance *string, token *string) {
	bot := &cobra.Command{Use: "bot", Short: "The Discord bot's access to an instance's channels"}
	root.AddCommand(bot)
	bot.AddCommand(&cobra.Command{
		Use:   "permissions",
		Short: "Check the bot's effective permissions in each channel bound to the instance",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if *instance == "" {
				return fmt.Errorf("--instance is required")
			}
			if *token == "" {
				return fmt.Errorf("set CASTAWAY_DISCORD_BOT_TOKEN (or discord_bot_token in the config file) to read the bot's permissions")
			}
			var res struct {
				Channels []botBinding `json:"channels"`
			}
			if err := call(c.Context(), "GET", "/instances/"+url.PathEscape(*instance)+"/discord-channels", nil, &res); err != nil {
				return err
			}
			if len(res.Channels) == 0 {
				_, err := fmt.Fprintln(c.OutOrStdout(), "No channels are bound to this instance.")
				return err
			}
			return checkBotPermissions(c.Context(), c.OutOrStdout(), *token, res.Channels)
		},
	})
}
