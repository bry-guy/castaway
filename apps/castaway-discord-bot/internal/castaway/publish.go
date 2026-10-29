package castaway

import (
	"context"
	"path"
)

// PublishTarget is a season channel an admin can post to (Name is the instance's short name).
type PublishTarget struct {
	Name      string `json:"name"`
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
}

// PublishRequest is a stored admin DM; empty Targets means the sender isn't an admin of any channel here.
type PublishRequest struct {
	RequestID *string         `json:"request_id"`
	Targets   []PublishTarget `json:"targets"`
}

// CreatePublishRequest records an admin's free-form DM (messageID keeps a redelivered DM from recording twice).
func (c *Client) CreatePublishRequest(ctx context.Context, adminID string, guildIDs []string, messageID, body string) (*PublishRequest, error) {
	var response PublishRequest
	err := c.doJSONBody(ctx, "POST", c.endpoint("/admin-dms/publish-requests"), nil, map[string]any{
		"admin_discord_user_id": adminID, "guild_ids": guildIDs, "request_key": messageID, "body": body,
	}, &response)
	return &response, err
}

// PublishReply is the outcome of an admin's reply: posted, cancelled, unknown, ambiguous, or closed.
type PublishReply struct {
	Status  string          `json:"status"`
	Posted  []PublishTarget `json:"posted"`
	Unknown []string        `json:"unknown"`
	Targets []PublishTarget `json:"targets"`
}

// ReplyPublishRequest applies an admin's reply ("Podracing/BrainLand", "no") to one of their publish requests.
func (c *Client) ReplyPublishRequest(ctx context.Context, requestID, adminID string, guildIDs []string, reply string) (*PublishReply, error) {
	var response PublishReply
	err := c.doJSONBody(ctx, "POST", c.endpoint(path.Join("/admin-dms/publish-requests", requestID, "reply")), nil, map[string]any{
		"admin_discord_user_id": adminID, "guild_ids": guildIDs, "reply": reply,
	}, &response)
	return &response, err
}
