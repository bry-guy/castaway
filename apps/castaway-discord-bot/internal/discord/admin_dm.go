package discord

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bry-guy/srvivor/apps/castaway-discord-bot/internal/messages"
	"github.com/bwmarrin/discordgo"
)

// Admin DMs: the bot DMs every instance admin about a problem draft or a post waiting for approval, and an
// admin answers by replying (Discord's Reply) to that DM. Each DM carries what a reply acts on, so the bot
// keeps no state:
//   - an approval DM ends with "approval <id> <revision>"; "yes" approves exactly that revision, and a reply
//     with complete new text replaces it (the new text gets its own approval DM).
//   - an alert with an action (like an episode import) ends with "action <id> <revision>"; "yes" runs exactly
//     that revision on the server.
//   - a draft DM links the player's post and (after a fix) quotes the working draft; "7. Thien An" lines
//     replace those ranks in the working draft (the post on first reply) and resubmit it.
//   - if the player already has a saved draft, the answer quotes the working draft and the saved draft's
//     fingerprint ("overwrite <fp>"); replying "overwrite" replaces exactly that saved draft.
//
// The server rejects stale actions: an approval of a changed post, or a fix after the draft was saved.
// The regexes below parse the ref lines that the templates in internal/messages write.

var (
	// Parses the link from draft-problem-dm.md.
	draftLink = regexp.MustCompile(`discord\.com/channels/\d+/(\d+)/(\d+)`)
	// Parses the approval token from approval-prompt.md and approval-missed.md.
	approvalRef = regexp.MustCompile("approval `([0-9a-f-]{36})` `([0-9a-f]{32})`")
	// Parses admin fix lines typed as replies; not a template.
	rankLine = regexp.MustCompile(`^\s*0*(\d{1,2})\s*[.):\-]?\s+(\S.*?)\s*$`)
	// Parses the working draft from draft-conflict.md and draft-problem-reply.md.
	workingCopy = regexp.MustCompile("(?s)Working draft:\n```\n(.*)\n```")
	// Parses the overwrite token from draft-conflict.md.
	overwriteOf = regexp.MustCompile("overwrite `([0-9a-f]{32})`")
	// Parses the action token from the admin-alert-action fragment in fragments.txt.
	actionRef = regexp.MustCompile("action `([0-9a-f-]{36})` `([0-9a-f]{32})`")
)

// dmAdmins sends text to each admin, reporting failures.
func (b *Bot) dmAdmins(ctx context.Context, admins []string, text string) error {
	var failed []string
	for _, admin := range admins {
		dm, err := b.session.UserChannelCreate(admin, discordgo.WithContext(ctx))
		if err == nil {
			_, err = b.session.ChannelMessageSendComplex(dm.ID, &discordgo.MessageSend{Content: text, AllowedMentions: allowedMentions(false), Flags: discordgo.MessageFlagsSuppressEmbeds}, discordgo.WithContext(ctx))
		}
		if err != nil {
			b.log.Error("admin DM", "admin", admin, "error", err)
			failed = append(failed, admin)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("couldn't DM admins %v", failed)
	}
	return nil
}

// applyFixes replaces (or adds) the draft's numbered lines with the reply's "N. Name" lines. It rejects a
// reply with no fixes, any line that isn't a fix, or the same rank twice, rather than guess.
func applyFixes(draft, reply string) (string, error) {
	fixes := map[int]string{}
	for _, line := range strings.Split(strings.TrimSpace(reply), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := rankLine.FindStringSubmatch(line)
		if m == nil {
			return "", messages.Err("fix-not-fix", map[string]any{"Line": strings.TrimSpace(line)})
		}
		rank, err := strconv.Atoi(m[1])
		if err != nil || rank < 1 {
			return "", messages.Err("fix-bad-rank", map[string]any{"Rank": m[1]})
		}
		if _, dup := fixes[rank]; dup {
			return "", messages.Err("fix-duplicate-rank", map[string]any{"Rank": rank})
		}
		fixes[rank] = m[2]
	}
	if len(fixes) == 0 {
		return "", messages.Err("fix-none", nil)
	}
	lines := strings.Split(draft, "\n")
	for i, line := range lines {
		if m := rankLine.FindStringSubmatch(line); m != nil {
			rank, err := strconv.Atoi(m[1])
			if name, ok := fixes[rank]; err == nil && ok {
				lines[i] = fmt.Sprintf("%d. %s", rank, name)
				delete(fixes, rank)
			}
		}
	}
	var added []int
	for rank := range fixes {
		added = append(added, rank)
	}
	sort.Ints(added)
	for _, rank := range added {
		lines = append(lines, fmt.Sprintf("%d. %s", rank, fixes[rank]))
	}
	return strings.Join(lines, "\n"), nil
}

// isReplacementCopy reports whether a non-"yes" reply is meant as new post text rather than a stray word
// like "no" or "wait": anything under 20 characters is treated as a question, not copy.
func isReplacementCopy(text string) bool {
	return utf8.RuneCountInString(text) >= 20
}

// handleAdminReply acts on a DM reply to one of the bot's own admin DMs.
// It reports whether the message was a recognized reply; an unrecognized DM falls through to the publish path.
func (b *Bot) handleAdminReply(ctx context.Context, m *discordgo.Message) (handled bool) {
	if m == nil || m.GuildID != "" || m.Author == nil || m.Author.Bot || m.MessageReference == nil || b.session.State == nil || b.session.State.User == nil {
		return
	}
	asked, err := b.session.ChannelMessage(m.ChannelID, m.MessageReference.MessageID, discordgo.WithContext(ctx))
	if err != nil || asked.Author == nil || asked.Author.ID != b.session.State.User.ID {
		return
	}
	answer := func(text string) {
		if _, err := b.session.ChannelMessageSendReply(m.ChannelID, text, m.Reference(), discordgo.WithContext(ctx)); err != nil {
			b.log.Error("admin DM answer", "error", err)
		}
	}
	handled = true
	if ref := publishRef.FindStringSubmatch(asked.Content); ref != nil {
		b.answerPublishReply(ctx, m, ref[1], answer)
		return
	}
	if ref := actionRef.FindStringSubmatch(asked.Content); ref != nil {
		if !strings.EqualFold(strings.Trim(strings.TrimSpace(m.Content), ".!"), "yes") {
			answer(messages.Fragment("action-hint", nil))
			return
		}
		if err := b.castaway.ApproveAdminAction(ctx, ref[1], ref[2], m.Author.ID); err != nil {
			answer(messages.Fragment("not-recorded", err.Error()))
			return
		}
		answer(messages.Fragment("action-recorded", nil))
		return
	}
	if ref := approvalRef.FindStringSubmatch(asked.Content); ref != nil {
		if !strings.EqualFold(strings.Trim(strings.TrimSpace(m.Content), ".!"), "yes") {
			text := strings.TrimSpace(m.Content)
			if !isReplacementCopy(text) {
				answer(messages.Fragment("approval-hint", nil))
				return
			}
			if err := b.castaway.ReviseAnnouncement(ctx, ref[1], ref[2], m.Author.ID, text); err != nil {
				answer(messages.Fragment("not-changed", err.Error()))
				return
			}
			answer(messages.Fragment("approval-revised", nil))
			return
		}
		at, err := b.castaway.ApproveAnnouncement(ctx, ref[1], ref[2], m.Author.ID)
		if err != nil {
			answer(messages.Fragment("not-approved", err.Error()))
			return
		}
		answer(messages.Fragment("approval-approved", map[string]any{"When": eastern(at)}))
		return
	}
	link := draftLink.FindStringSubmatch(asked.Content)
	if link == nil {
		handled = false
		return
	}
	original, err := b.session.ChannelMessage(link[1], link[2], discordgo.WithContext(ctx))
	if err != nil {
		answer(messages.Fragment("draft-read-failed", err.Error()))
		return
	}
	working := original.Content
	if w := workingCopy.FindStringSubmatch(asked.Content); w != nil {
		working = w[1]
	}
	var fixed, overwrite string
	if ref := overwriteOf.FindStringSubmatch(asked.Content); ref != nil && strings.EqualFold(strings.Trim(strings.TrimSpace(m.Content), ".!"), "overwrite") {
		fixed, overwrite = working, ref[1]
	} else if fixed, err = applyFixes(working, m.Content); err != nil {
		answer(messages.Fragment("nothing-changed", err.Error()))
		return
	}
	result, err := b.castaway.FixDraftThreadMessage(ctx, link[1], original.ID, "fix-"+m.ID, original.Author.ID, fixed, m.Author.ID, overwrite)
	if err != nil {
		answer(messages.Fragment("nothing-changed", err.Error()))
		return
	}
	post := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", original.GuildID, link[1], link[2])
	switch result.Status {
	case "conflict":
		text := messages.Fragment("draft-conflict.md", map[string]any{
			"Mention": original.Author.Mention(), "Post": post, "Draft": fixed, "SavedPicks": result.SavedPicks,
		})
		if len(text) > 2000 {
			answer(messages.Fragment("draft-conflict-too-long", nil))
			return
		}
		answer(text)
	case "saved", "unchanged":
		answer(messages.Fragment("draft-saved", map[string]any{"Player": result.Player}))
	case "problem":
		text := messages.Fragment("draft-problem-reply.md", map[string]any{
			"Player": result.Player, "Post": post, "Problems": strings.Join(result.Problems, "\n- "), "Draft": fixed,
		})
		if len(text) > 2000 {
			answer(messages.Fragment("draft-problem-too-long", nil))
			return
		}
		answer(text)
	default:
		answer(messages.Fragment("draft-unrecognized", map[string]any{"Status": result.Status}))
	}
	return
}

// eastern formats a send time for admins, in Eastern: "Wed Oct 21 8:00pm ET".
func eastern(t time.Time) string {
	if loc, err := time.LoadLocation("America/New_York"); err == nil {
		t = t.In(loc)
	}
	return t.Format("Mon Jan 2 3:04pm") + " ET"
}
