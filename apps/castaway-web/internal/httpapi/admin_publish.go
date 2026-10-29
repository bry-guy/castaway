package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// publishTarget is one season channel an admin can post to: an instance they administer, bound to a channel in
// one of the bot's guilds. Name is the instance's short name; the channel ID also works as a reply.
type publishTarget struct {
	InstanceID int64  `json:"-"`
	Name       string `json:"name"`
	GuildID    string `json:"guild_id"`
	ChannelID  string `json:"channel_id"`
}

// publishRequestWindow is how long a publish request stays open for the admin's reply.
const publishRequestWindow = 24 * time.Hour

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func publishTargets(ctx context.Context, q querier, adminID string, guildIDs []string) ([]publishTarget, error) {
	rows, err := q.Query(ctx, `
		SELECT i.id, i.name, b.guild_id, b.channel_id FROM discord_channel_bindings b JOIN instances i ON i.id = b.instance_id
		WHERE b.guild_id = ANY($1::text[]) AND EXISTS (SELECT 1 FROM instance_admins a WHERE a.instance_id = i.id AND a.discord_user_id = $2)
		ORDER BY i.name, b.guild_id, b.channel_id`, guildIDs, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []publishTarget
	for rows.Next() {
		var t publishTarget
		if err := rows.Scan(&t.InstanceID, &t.Name, &t.GuildID, &t.ChannelID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// resolvePublishReply maps "Podracing/BrainLand" (case-insensitive; a channel ID also works) to targets. A name
// shared by two targets is ambiguous, and any unknown part makes the whole reply unknown; nothing is half-chosen.
func resolvePublishReply(reply string, targets []publishTarget) (chosen []publishTarget, unknown []string, ambiguous bool) {
	seen := map[publishTarget]bool{}
	for _, part := range strings.Split(reply, "/") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var matches []publishTarget
		for _, t := range targets {
			if strings.EqualFold(t.Name, part) || t.ChannelID == part {
				matches = append(matches, t)
			}
		}
		switch {
		case len(matches) == 0:
			unknown = append(unknown, part)
		case len(matches) > 1:
			ambiguous = true
		case !seen[matches[0]]:
			seen[matches[0]] = true
			chosen = append(chosen, matches[0])
		}
	}
	return chosen, unknown, ambiguous
}

// createPublishRequest records an admin's free-form DM and returns the channels it could post to. A DM from a
// non-admin (or with no channels in these guilds) returns no targets and records nothing.
func (s *Server) createPublishRequest(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	var req struct {
		AdminDiscordUserID string   `json:"admin_discord_user_id"`
		GuildIDs           []string `json:"guild_ids"`
		RequestKey         string   `json:"request_key"` // the admin's DM message ID, so a redelivered DM isn't recorded twice
		Body               string   `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !validAnnouncementID(req.AdminDiscordUserID) || !validAnnouncementID(req.RequestKey) ||
		len(req.GuildIDs) == 0 || strings.TrimSpace(req.Body) == "" || utf8.RuneCountInString(req.Body) > 2000 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "admin_discord_user_id, request_key, guild_ids and a nonblank body of at most 2000 characters are required"})
		return
	}
	for _, guildID := range req.GuildIDs {
		if !validAnnouncementID(guildID) {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid guild id"})
			return
		}
	}
	targets, err := publishTargets(c.Request.Context(), s.pool, req.AdminDiscordUserID, req.GuildIDs)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if len(targets) == 0 {
		c.JSON(http.StatusOK, gin.H{"request_id": nil, "targets": []publishTarget{}})
		return
	}
	var id uuid.UUID
	err = s.pool.QueryRow(c.Request.Context(), `
		INSERT INTO admin_publish_requests (admin_discord_user_id, request_key, body, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (admin_discord_user_id, request_key) DO UPDATE SET request_key = EXCLUDED.request_key
		RETURNING id`, req.AdminDiscordUserID, req.RequestKey, req.Body, s.now()).Scan(&id)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"request_id": id, "targets": targets})
}

// replyPublishRequest applies an admin's reply to a publish request: "no"/"cancel" cancels; server names post
// one announcement per named channel, due now; an unknown or ambiguous name keeps the request open.
func (s *Server) replyPublishRequest(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	id, err := uuid.Parse(c.Param("requestID"))
	var req struct {
		AdminDiscordUserID string   `json:"admin_discord_user_id"`
		GuildIDs           []string `json:"guild_ids"`
		Reply              string   `json:"reply"`
	}
	if err != nil || c.ShouldBindJSON(&req) != nil || !validAnnouncementID(req.AdminDiscordUserID) || len(req.GuildIDs) == 0 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "request id, admin_discord_user_id and guild_ids are required"})
		return
	}
	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	var status, body string
	var createdAt time.Time
	err = tx.QueryRow(c.Request.Context(), `SELECT status, body, created_at FROM admin_publish_requests
		WHERE id = $1 AND admin_discord_user_id = $2 FOR UPDATE`, id, req.AdminDiscordUserID).Scan(&status, &body, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "publish request not found for this admin"})
		return
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	targets, err := publishTargets(c.Request.Context(), tx, req.AdminDiscordUserID, req.GuildIDs)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if status != "pending" || s.now().Sub(createdAt) > publishRequestWindow {
		c.JSON(http.StatusOK, gin.H{"status": "closed", "targets": targets})
		return
	}
	reply := strings.ToLower(strings.TrimSpace(req.Reply))
	if reply == "no" || reply == "cancel" {
		if _, err := tx.Exec(c.Request.Context(), `UPDATE admin_publish_requests SET status = 'cancelled', decided_at = $2 WHERE id = $1`, id, s.now()); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		if err := tx.Commit(c.Request.Context()); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "cancelled", "targets": targets})
		return
	}
	chosen, unknown, ambiguous := resolvePublishReply(req.Reply, targets)
	if ambiguous {
		c.JSON(http.StatusOK, gin.H{"status": "ambiguous", "unknown": unknown, "targets": targets})
		return
	}
	if len(unknown) > 0 || len(chosen) == 0 {
		c.JSON(http.StatusOK, gin.H{"status": "unknown", "unknown": unknown, "targets": targets})
		return
	}
	for _, t := range chosen {
		if _, err := tx.Exec(c.Request.Context(), `
			INSERT INTO announcements (instance_id, guild_id, channel_id, request_key, body, due_at, status, notify_users)
			VALUES ($1, $2, $3, $4, $5, $6, 'pending', false) ON CONFLICT (instance_id, request_key) DO NOTHING`,
			t.InstanceID, t.GuildID, t.ChannelID, "admin-dm:"+id.String(), body, s.now()); err != nil {
			writeAnnouncementError(c, err)
			return
		}
	}
	if _, err := tx.Exec(c.Request.Context(), `UPDATE admin_publish_requests SET status = 'posted', decided_at = $2 WHERE id = $1`, id, s.now()); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "posted", "posted": chosen, "targets": targets})
}
