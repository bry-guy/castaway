package discord

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-discord-bot/internal/messages"
	"github.com/bwmarrin/discordgo"
)

// draftThreads is the set of watched thread IDs, refreshed from the API.
type draftThreads struct {
	mu  sync.RWMutex
	ids map[string]bool
}

func (d *draftThreads) has(id string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.ids[id]
}

// watchDraftThreads refreshes the watched threads every minute and replays each newly watched thread's
// history once, so posts made while the bot was down are picked up (the API skips ones it has seen).
func (b *Bot) watchDraftThreads(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		threads, err := b.castaway.ListDraftThreads(ctx)
		if err != nil && ctx.Err() == nil {
			b.log.Error("list draft threads", "error", err)
		}
		if err == nil {
			ids := map[string]bool{}
			for _, t := range threads {
				ids[t.ThreadID] = true
			}
			b.drafts.mu.Lock()
			fresh := []string{}
			for id := range ids {
				if !b.drafts.ids[id] {
					fresh = append(fresh, id)
				}
			}
			b.drafts.ids = ids
			b.drafts.mu.Unlock()
			for _, id := range fresh {
				b.replayDraftThread(ctx, id)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *Bot) replayDraftThread(ctx context.Context, threadID string) {
	var all []*discordgo.Message
	before := ""
	for {
		page, err := b.session.ChannelMessages(threadID, 100, before, "", "", discordgo.WithContext(ctx))
		if err != nil {
			b.log.Error("read draft thread", "thread_id", threadID, "error", err)
			return
		}
		all = append(all, page...)
		if len(page) < 100 {
			break
		}
		before = page[len(page)-1].ID
	}
	slices.Reverse(all) // oldest first, so submission order follows posting order
	for _, m := range all {
		b.handleDraftMessage(ctx, m)
	}
}

func (b *Bot) onMessageCreate(_ *discordgo.Session, m *discordgo.MessageCreate) {
	ctx := context.Background()
	if b.handleAdminReply(ctx, m.Message) {
		return
	}
	b.handlePublishRequest(ctx, m.Message)
	b.handleChampionReply(ctx, m.Message)
	b.handleDraftMessage(ctx, m.Message)
}

func (b *Bot) onMessageUpdate(_ *discordgo.Session, m *discordgo.MessageUpdate) {
	b.handleDraftMessage(context.Background(), m.Message)
}

// handleDraftMessage forwards a watched-thread post to the API and DMs the admin about each problem.
func (b *Bot) handleDraftMessage(ctx context.Context, m *discordgo.Message) {
	if m == nil || m.Author == nil || m.Author.Bot || m.Content == "" || !b.drafts.has(m.ChannelID) {
		return
	}
	if m.GuildID != "" && !slices.Contains(b.targetServerIDs, m.GuildID) {
		return
	}
	version := m.Timestamp
	if m.EditedTimestamp != nil {
		version = *m.EditedTimestamp
	}
	result, err := b.castaway.PostDraftThreadMessage(ctx, m.ChannelID, m.ID, version.UTC().Format(time.RFC3339Nano), m.Author.ID, m.Content)
	if err != nil {
		b.log.Error("draft thread message", "message_id", m.ID, "error", err)
		return
	}
	b.log.Info("draft thread message", "message_id", m.ID, "status", result.Status, "player", result.Player)
	if result.Status != "problem" {
		return
	}
	guild := m.GuildID
	if guild == "" {
		if channel, err := b.session.State.Channel(m.ChannelID); err == nil {
			guild = channel.GuildID
		}
	}
	text := messages.Fragment("draft-problem-dm.md", map[string]any{"Player": result.Player, "Guild": guild, "ChannelID": m.ChannelID, "MessageID": m.ID, "Problems": strings.Join(result.Problems, "\n- ")})
	hint := messages.Fragment("draft-problem-hint", nil)
	if len(text)+len(hint) > 2000 {
		text = text[:1990-len(hint)] + messages.Fragment("draft-truncated", nil)
	}
	admins := result.Admins
	if len(admins) == 0 {
		admins = []string{b.adminContactID}
	}
	if err := b.dmAdmins(ctx, admins, text+hint); err != nil {
		b.log.Error("DM draft problem", "message_id", m.ID, "error", err)
	}
}
