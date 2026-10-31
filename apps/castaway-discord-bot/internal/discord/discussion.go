package discord

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-discord-bot/internal/messages"
	"github.com/bwmarrin/discordgo"
)

// deliverDiscussionThread opens the next due episode discussion thread: a public thread with no opener, the
// system "started a thread" message removed from the channel, then one plain line naming the thread. Any failure
// is DM'd to the admins once; the server never retries it, so nothing opens twice.
func (b *Bot) deliverDiscussionThread(ctx context.Context) error {
	claim, err := b.castaway.ClaimDiscussionThread(ctx, b.targetServerIDs)
	if err != nil || claim == nil {
		return err
	}
	d := claim.Discussion
	threadID := ""
	fail := func(reason string) error {
		ackCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		recordErr := b.castaway.FinishDiscussionThread(ackCtx, d.ID, threadID, "", reason)
		admins := claim.Admins
		if len(admins) == 0 {
			admins = []string{b.adminContactID}
		}
		text := messages.Fragment("discussion-failed", map[string]any{"Name": d.Name, "ChannelID": d.ChannelID, "Reason": reason, "ThreadID": threadID})
		return errors.Join(fmt.Errorf("discussion %s (%s) failed: %s", d.Name, d.ID, reason), recordErr, b.dmAdmins(ctx, admins, text))
	}
	if !slices.Contains(b.targetServerIDs, d.GuildID) {
		return fail("guild is not allowed")
	}
	bound, err := b.castaway.GetChannelInstance(ctx, d.GuildID, d.ChannelID)
	if err != nil {
		return fail(err.Error())
	}
	if bound != d.InstanceID {
		return fail("channel is no longer bound to this instance")
	}
	missing, err := b.missingDiscussionPermissions(d.ChannelID)
	if err != nil {
		return fail(err.Error())
	}
	if len(missing) > 0 {
		return fail("the bot is missing " + strings.Join(missing, ", ") + " in that channel")
	}
	// Retries are off for both sends: a retried request after a lost response could open or post twice.
	thread, err := b.session.ThreadStartComplex(d.ChannelID, &discordgo.ThreadStart{Name: d.Name, Type: discordgo.ChannelTypeGuildPublicThread, AutoArchiveDuration: 10080}, discordgo.WithRestRetries(0))
	if err != nil {
		return fail("opening the thread: " + err.Error())
	}
	threadID = thread.ID
	if err := b.removeThreadStartedMessage(d.ChannelID, threadID); err != nil {
		return fail(err.Error())
	}
	line, err := b.session.ChannelMessageSendComplex(d.ChannelID, &discordgo.MessageSend{
		Content:         "<#" + threadID + ">",
		AllowedMentions: allowedMentions(false),
		Flags:           discordgo.MessageFlagsSuppressEmbeds,
	}, discordgo.WithRestRetries(0))
	if err != nil {
		return fail("posting the line: " + err.Error())
	}
	if err := b.castaway.FinishDiscussionThread(ctx, d.ID, threadID, line.ID, ""); err != nil {
		return fmt.Errorf("discussion %s posted as %s but recording it failed: %w", d.ID, line.ID, err)
	}
	b.log.Info("discussion thread opened", "discussion_id", d.ID, "thread_id", threadID, "channel_id", d.ChannelID)
	return nil
}

// missingDiscussionPermissions names what the bot lacks in the channel for the thread and its line.
func (b *Bot) missingDiscussionPermissions(channelID string) ([]string, error) {
	perms, err := b.session.UserChannelPermissions(b.session.State.User.ID, channelID)
	if err != nil {
		return nil, fmt.Errorf("couldn't read the bot's permissions: %w", err)
	}
	var missing []string
	for _, need := range []struct {
		bit  int64
		name string
	}{
		{discordgo.PermissionViewChannel, "View Channel"},
		{discordgo.PermissionSendMessages, "Send Messages"},
		{discordgo.PermissionCreatePublicThreads, "Create Public Threads"},
		{discordgo.PermissionManageMessages, "Manage Messages"},
	} {
		if perms&need.bit == 0 {
			missing = append(missing, need.name)
		}
	}
	return missing, nil
}

// removeThreadStartedMessage deletes the channel's "started a thread" system message for threadID. It can lag the
// thread by a moment, so it looks a few times before giving up.
func (b *Bot) removeThreadStartedMessage(channelID, threadID string) error {
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		msgs, err := b.session.ChannelMessages(channelID, 25, "", "", "", discordgo.WithRestRetries(0))
		if err != nil {
			return fmt.Errorf("reading the channel for the thread message: %w", err)
		}
		for _, m := range msgs {
			if m.Type == discordgo.MessageTypeThreadCreated && (m.ID == threadID || (m.Thread != nil && m.Thread.ID == threadID)) {
				if err := b.session.ChannelMessageDelete(channelID, m.ID, discordgo.WithRestRetries(0)); err != nil {
					return fmt.Errorf("deleting the thread message: %w", err)
				}
				return nil
			}
		}
	}
	return errors.New("the thread's started message wasn't found to delete")
}
