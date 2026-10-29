package discord

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/bry-guy/srvivor/apps/castaway-discord-bot/internal/castaway"
	"github.com/bry-guy/srvivor/apps/castaway-discord-bot/internal/messages"
	"github.com/bwmarrin/discordgo"
)

// publishRef parses the request ID from the publish prompt; a reply to it names the server(s) to post to.
var publishRef = regexp.MustCompile("publish `([0-9a-f-]{36})`")

// handlePublishRequest turns an admin's free-form DM into a publish request: it echoes the exact text, lists the
// season channels it could post to, and asks which one(s). Anyone else (no admin channels here) gets nothing.
func (b *Bot) handlePublishRequest(ctx context.Context, m *discordgo.Message) {
	if m == nil || m.GuildID != "" || m.Author == nil || m.Author.Bot || strings.TrimSpace(m.Content) == "" {
		return
	}
	req, err := b.castaway.CreatePublishRequest(ctx, m.Author.ID, b.targetServerIDs, m.ID, m.Content)
	if err != nil {
		b.log.Error("publish request", "message_id", m.ID, "error", err)
		return
	}
	if req.RequestID == nil || len(req.Targets) == 0 {
		return
	}
	// The text goes out exactly as written (no pings, no link previews); the prompt names the targets.
	if _, err := b.session.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{Content: m.Content, AllowedMentions: allowedMentions(false), Flags: discordgo.MessageFlagsSuppressEmbeds}, discordgo.WithContext(ctx)); err != nil {
		b.log.Error("publish echo", "error", err)
		return
	}
	prompt := messages.Fragment("publish-prompt.md", map[string]any{
		"Targets": publishOptions(req.Targets), "Example": exampleNames(req.Targets), "ID": *req.RequestID,
	})
	if _, err := b.session.ChannelMessageSendComplex(m.ChannelID, &discordgo.MessageSend{Content: prompt, AllowedMentions: allowedMentions(false)}, discordgo.WithContext(ctx)); err != nil {
		b.log.Error("publish prompt", "error", err)
	}
}

// answerPublishReply applies a reply to a publish prompt and reports the outcome in the same DM.
func (b *Bot) answerPublishReply(ctx context.Context, m *discordgo.Message, requestID string, answer func(string)) {
	reply, err := b.castaway.ReplyPublishRequest(ctx, requestID, m.Author.ID, b.targetServerIDs, m.Content)
	if err != nil {
		answer(messages.Fragment("not-recorded", err.Error()))
		return
	}
	switch reply.Status {
	case "posted":
		answer(messages.Fragment("publish-posted", map[string]any{"Names": joinNames(reply.Posted)}))
	case "cancelled":
		answer(messages.Fragment("publish-cancelled", nil))
	case "ambiguous":
		answer(messages.Fragment("publish-ambiguous", map[string]any{"Options": publishOptions(reply.Targets)}))
	case "unknown":
		answer(messages.Fragment("publish-unknown", map[string]any{"Unknown": strings.Join(reply.Unknown, ", "), "Options": publishOptions(reply.Targets)}))
	default:
		answer(messages.Fragment("publish-closed", nil))
	}
}

// publishOptions lists each target as "- Name: <#channel>" for admins to read and reply with.
func publishOptions(targets []castaway.PublishTarget) string {
	var lines []string
	for _, t := range targets {
		lines = append(lines, fmt.Sprintf("- %s: <#%s>", t.Name, t.ChannelID))
	}
	return strings.Join(lines, "\n")
}

func exampleNames(targets []castaway.PublishTarget) string {
	var names []string
	for _, t := range targets {
		names = append(names, t.Name)
	}
	return strings.Join(names, "/")
}

func joinNames(targets []castaway.PublishTarget) string {
	var names []string
	for _, t := range targets {
		names = append(names, t.Name)
	}
	return strings.Join(names, " and ")
}
