package castaway

import (
	"context"
	"path"
	"time"
)

// Discussion is an episode discussion thread the bot should open now.
type Discussion struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	GuildID    string    `json:"guild_id"`
	ChannelID  string    `json:"channel_id"`
	Name       string    `json:"name"`
	DueAt      time.Time `json:"due_at"`
}

// DiscussionClaim leases one due discussion thread, with the instance's admins to tell if it fails.
type DiscussionClaim struct {
	Discussion *Discussion `json:"discussion"`
	Admins     []string    `json:"admin_discord_user_ids"`
}

// ClaimDiscussionThread returns the next due discussion thread for these guilds, or nil.
func (c *Client) ClaimDiscussionThread(ctx context.Context, guildIDs []string) (*DiscussionClaim, error) {
	var response DiscussionClaim
	err := c.doJSONBody(ctx, "POST", c.endpoint("/discussion-threads/claim"), nil, map[string]any{"guild_ids": guildIDs}, &response)
	if err != nil || response.Discussion == nil {
		return nil, err
	}
	return &response, nil
}

// FinishDiscussionThread records the outcome: a thread and line message when sent, else the failure (and the
// thread, if one was opened).
func (c *Client) FinishDiscussionThread(ctx context.Context, id, threadID, lineMessageID, failure string) error {
	var response map[string]any
	return c.doJSONBody(ctx, "POST", c.endpoint(path.Join("/discussion-threads", id, "finish")), nil, map[string]any{
		"thread_id": threadID, "line_message_id": lineMessageID, "failure": failure,
	}, &response)
}
