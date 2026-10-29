package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// discussionLease is how long a claimed thread may run before it's treated as interrupted (and failed, not retried).
const discussionLease = 10 * time.Minute

// upsertDiscussionThread queues one episode's discussion thread (probst, at its air time). Queuing the same key
// again changes nothing, except that a thread Discord refused (and no thread was opened) is queued again.
func (s *Server) upsertDiscussionThread(c *gin.Context) {
	instanceID, ok := s.automationInstance(c)
	if !ok {
		return
	}
	key := c.Param("requestKey")
	var req struct {
		GuildID   string    `json:"guild_id"`
		ChannelID string    `json:"channel_id"`
		Name      string    `json:"name"`
		DueAt     time.Time `json:"due_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !validAnnouncementID(req.GuildID) || !validAnnouncementID(req.ChannelID) || len(key) == 0 || len(key) > 128 ||
		strings.TrimSpace(req.Name) == "" || utf8.RuneCountInString(req.Name) > 100 || req.DueAt.IsZero() {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid discussion thread"})
		return
	}
	ctx := c.Request.Context()
	var bound bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM discord_channel_bindings b JOIN instances i ON i.id = b.instance_id
		WHERE i.public_id = $1 AND b.guild_id = $2 AND b.channel_id = $3)`, toPGUUID(instanceID), req.GuildID, req.ChannelID).Scan(&bound); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if !bound {
		c.JSON(http.StatusConflict, errorResponse{Error: "channel is not bound to this instance"})
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	var guildID, channelID, name, status string
	var threadID *string
	var interrupted bool // a lease expired mid-claim: it may already have opened a thread, so it's never queued again
	err = tx.QueryRow(ctx, `SELECT d.guild_id, d.channel_id, d.name, d.status, d.thread_id, d.claimed_at IS NOT NULL FROM discussion_threads d JOIN instances i ON i.id = d.instance_id
		WHERE i.public_id = $1 AND d.request_key = $2 FOR UPDATE OF d`, toPGUUID(instanceID), key).Scan(&guildID, &channelID, &name, &status, &threadID, &interrupted)
	action := "unchanged"
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `INSERT INTO discussion_threads (instance_id, request_key, guild_id, channel_id, name, due_at)
			SELECT id, $2, $3, $4, $5, $6 FROM instances WHERE public_id = $1`, toPGUUID(instanceID), key, req.GuildID, req.ChannelID, req.Name, req.DueAt.UTC()); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		action = "created"
	case err != nil:
		writeAnnouncementError(c, err)
		return
	case guildID != req.GuildID || channelID != req.ChannelID || name != req.Name:
		c.JSON(http.StatusConflict, errorResponse{Error: "key already queued for a different channel or name"})
		return
	case status == "failed" && threadID == nil && !interrupted:
		if _, err := tx.Exec(ctx, `UPDATE discussion_threads SET status = 'pending', due_at = $2, failure = NULL WHERE request_key = $1 AND instance_id = (SELECT id FROM instances WHERE public_id = $3)`,
			key, req.DueAt.UTC(), toPGUUID(instanceID)); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		action = "requeued"
	}
	if err := tx.Commit(ctx); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"action": action})
}

// claimDiscussionThread leases the next due thread for the bot to open, with the instance's admins to DM on failure.
func (s *Server) claimDiscussionThread(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	var req struct {
		GuildIDs []string `json:"guild_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.GuildIDs) == 0 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "guild_ids are required"})
		return
	}
	for _, guildID := range req.GuildIDs {
		if !validAnnouncementID(guildID) {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid guild id"})
			return
		}
	}
	ctx := c.Request.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	now := s.now()
	if _, err := tx.Exec(ctx, `UPDATE discussion_threads SET status = 'failed', failure = 'interrupted before it finished; check Discord for a thread'
		WHERE status = 'sending' AND claimed_at < $1 AND guild_id = ANY($2::text[])`, now.Add(-discussionLease), req.GuildIDs); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	var id, instanceID pgtype.UUID
	var out struct {
		ID        uuid.UUID `json:"id"`
		Instance  uuid.UUID `json:"instance_id"`
		GuildID   string    `json:"guild_id"`
		ChannelID string    `json:"channel_id"`
		Name      string    `json:"name"`
		DueAt     time.Time `json:"due_at"`
	}
	err = tx.QueryRow(ctx, `SELECT d.id, i.public_id, d.guild_id, d.channel_id, d.name, d.due_at FROM discussion_threads d JOIN instances i ON i.id = d.instance_id
		WHERE d.status = 'pending' AND d.due_at <= $1 AND d.guild_id = ANY($2::text[]) ORDER BY d.due_at, d.id LIMIT 1 FOR UPDATE OF d SKIP LOCKED`, now, req.GuildIDs).
		Scan(&id, &instanceID, &out.GuildID, &out.ChannelID, &out.Name, &out.DueAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"discussion": nil})
		return
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE discussion_threads SET status = 'sending', claimed_at = $2 WHERE id = $1`, id, now); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	rows, err := tx.Query(ctx, `SELECT a.discord_user_id FROM instance_admins a JOIN instances i ON i.id = a.instance_id WHERE i.public_id = $1 ORDER BY a.discord_user_id`, instanceID)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	admins, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	out.ID, out.Instance = uuid.UUID(id.Bytes), uuid.UUID(instanceID.Bytes)
	out.DueAt = out.DueAt.UTC()
	c.JSON(http.StatusOK, gin.H{"discussion": out, "admin_discord_user_ids": admins})
}

// finishDiscussionThread records the outcome of a claim: sent (thread and line posted) or failed (with the reason,
// and the thread if one was opened). A failed row is never retried by the bot.
func (s *Server) finishDiscussionThread(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	id, err := uuid.Parse(c.Param("discussionID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid discussion id"})
		return
	}
	var req struct {
		ThreadID      string `json:"thread_id"`
		LineMessageID string `json:"line_message_id"`
		Failure       string `json:"failure"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.ThreadID != "" && !validAnnouncementID(req.ThreadID)) ||
		(req.Failure == "" && (!validAnnouncementID(req.ThreadID) || !validAnnouncementID(req.LineMessageID))) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "a thread and line message are required unless it failed"})
		return
	}
	status := "sent"
	if req.Failure != "" {
		status = "failed"
	}
	result, err := s.pool.Exec(c.Request.Context(), `UPDATE discussion_threads SET status = $2, thread_id = NULLIF($3, ''), line_message_id = NULLIF($4, ''), failure = NULLIF($5, ''), claimed_at = NULL
		WHERE id = $1 AND status = 'sending'`, id, status, req.ThreadID, req.LineMessageID, req.Failure)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if result.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, errorResponse{Error: "discussion thread is not awaiting delivery"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": status})
}
