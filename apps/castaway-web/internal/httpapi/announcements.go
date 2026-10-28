package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type announcementRecord struct {
	ID          uuid.UUID  `json:"id"`
	InstanceID  uuid.UUID  `json:"instance_id"`
	GuildID     string     `json:"guild_id"`
	ChannelID   string     `json:"channel_id"`
	RequestKey  string     `json:"request_key"`
	Body        string     `json:"body"`
	ScheduledAt *time.Time `json:"scheduled_at"`
	DueAt       time.Time  `json:"due_at"`
	Status      string     `json:"status"`
	MessageID   *string    `json:"message_id"`
	NotifyUsers bool       `json:"notify_users"`
	// Approval gate: a gated draft waits for an admin's "yes" (status draft until then). ApprovalNotifiedAt is when
	// the approval DM was delivered; ApprovalSendAt is the time the post sends once approved.
	ApprovalGated      bool       `json:"approval_gated"`
	ApprovalSendAt     *time.Time `json:"approval_send_at"`
	ApprovalNotifiedAt *time.Time `json:"approval_notified_at"`
	// Thread, when set, sends the body inside a shared bot thread; ID is empty until the thread exists.
	Thread *announcementThread `json:"thread,omitempty"`
}

type announcementThread struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Starter string `json:"starter"`
	ID      string `json:"id,omitempty"`
}

func validAnnouncementID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n != 0
}

func scanAnnouncement(row pgx.Row) (announcementRecord, error) {
	var a announcementRecord
	var id, instanceID pgtype.UUID
	err := row.Scan(&id, &instanceID, &a.GuildID, &a.ChannelID, &a.RequestKey, &a.Body, &a.ScheduledAt, &a.DueAt, &a.Status, &a.MessageID, &a.NotifyUsers,
		&a.ApprovalGated, &a.ApprovalSendAt, &a.ApprovalNotifiedAt, &a.Thread)
	if err == nil {
		a.ID, a.InstanceID = uuid.UUID(id.Bytes), uuid.UUID(instanceID.Bytes)
		a.DueAt = a.DueAt.UTC()
		for _, t := range []**time.Time{&a.ScheduledAt, &a.ApprovalSendAt, &a.ApprovalNotifiedAt} {
			if *t != nil {
				utc := (*t).UTC()
				*t = &utc
			}
		}
	}
	return a, err
}

const announcementColumns = `a.id, i.public_id, a.guild_id, a.channel_id, a.request_key, a.body, a.scheduled_at, a.due_at, a.status, a.message_id, a.notify_users,
	a.approval_gated, a.approval_send_at, a.approval_notified_at,
	CASE WHEN a.thread IS NULL THEN NULL ELSE a.thread || jsonb_build_object('id', (SELECT t.thread_id FROM announcement_threads t
		WHERE t.instance_id = a.instance_id AND t.channel_id = a.channel_id AND t.thread_key = a.thread->>'key')) END`

func (s *Server) createAnnouncement(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return
	}
	var req struct {
		GuildID     string     `json:"guild_id"`
		ChannelID   string     `json:"channel_id"`
		RequestKey  string     `json:"request_key"`
		Body        string     `json:"body"`
		ScheduledAt *time.Time `json:"scheduled_at"`
		Draft       bool       `json:"draft"`
		NotifyUsers bool       `json:"notify_users"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Draft && req.ScheduledAt != nil) || !validAnnouncementID(req.GuildID) || !validAnnouncementID(req.ChannelID) || len(req.RequestKey) == 0 || len(req.RequestKey) > 128 || utf8.RuneCountInString(req.Body) > 2000 || strings.TrimSpace(req.Body) == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid announcement"})
		return
	}
	if req.ScheduledAt != nil {
		v := req.ScheduledAt.UTC().Truncate(time.Microsecond)
		req.ScheduledAt = &v
	}
	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	q := s.queries.WithTx(tx)
	if err := q.LockDiscordChannel(c.Request.Context(), req.GuildID+":"+req.ChannelID); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if _, err := q.LockInstanceForProgression(c.Request.Context(), toPGUUID(instanceID)); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if err := requireWordleInstanceAdmin(c.Request.Context(), q, toPGUUID(instanceID), c.Request); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	binding, err := q.GetDiscordChannelBinding(c.Request.Context(), db.GetDiscordChannelBindingParams{GuildID: req.GuildID, ChannelID: req.ChannelID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && binding.InstanceID != toPGUUID(instanceID)) {
		c.JSON(http.StatusConflict, errorResponse{Error: "channel is not bound to this instance"})
		return
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	a, err := scanAnnouncement(tx.QueryRow(c.Request.Context(), `SELECT `+announcementColumns+` FROM announcements a JOIN instances i ON i.id = a.instance_id WHERE i.public_id = $1 AND a.request_key = $2`, toPGUUID(instanceID), req.RequestKey))
	if errors.Is(err, pgx.ErrNoRows) {
		if req.ScheduledAt != nil && req.ScheduledAt.Before(s.now()) {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "scheduled_at must be in the future"})
			return
		}
		_, err = tx.Exec(c.Request.Context(), `
			INSERT INTO announcements (instance_id, guild_id, channel_id, request_key, body, scheduled_at, due_at, status, notify_users)
			SELECT id, $2, $3, $4, $5, $6::timestamptz, COALESCE($6::timestamptz, $7::timestamptz), CASE WHEN $8::boolean THEN 'draft' ELSE 'pending' END, $9 FROM instances WHERE public_id = $1
			ON CONFLICT (instance_id, request_key) DO NOTHING`, toPGUUID(instanceID), req.GuildID, req.ChannelID, req.RequestKey, req.Body, req.ScheduledAt, s.now(), req.Draft, req.NotifyUsers)
		if err == nil {
			a, err = scanAnnouncement(tx.QueryRow(c.Request.Context(), `SELECT `+announcementColumns+` FROM announcements a JOIN instances i ON i.id = a.instance_id WHERE i.public_id = $1 AND a.request_key = $2`, toPGUUID(instanceID), req.RequestKey))
		}
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if a.GuildID != req.GuildID || a.ChannelID != req.ChannelID || a.Body != req.Body || (a.Status == "draft") != req.Draft || a.NotifyUsers != req.NotifyUsers || (a.ScheduledAt == nil) != (req.ScheduledAt == nil) || (a.ScheduledAt != nil && !a.ScheduledAt.Equal(*req.ScheduledAt)) {
		c.JSON(http.StatusConflict, errorResponse{Error: "request_key already used with different announcement"})
		return
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"announcement": a})
}

func (s *Server) claimAnnouncement(c *gin.Context) {
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
	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	if _, err := tx.Exec(c.Request.Context(), `
		UPDATE announcements SET status = 'failed'
		WHERE status = 'sending' AND claimed_at < $1 AND guild_id = ANY($2::text[])`, s.now().Add(-10*time.Minute), req.GuildIDs); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	commitEmpty := func() {
		if err := tx.Commit(c.Request.Context()); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"announcement": nil})
	}
	var candidate pgtype.UUID
	var guildID, channelID string
	err = tx.QueryRow(c.Request.Context(), `
		SELECT a.id, a.guild_id, a.channel_id FROM announcements a JOIN discord_channel_bindings b
		ON b.instance_id = a.instance_id AND b.guild_id = a.guild_id AND b.channel_id = a.channel_id
		WHERE a.status = 'pending' AND a.due_at <= $1 AND a.guild_id = ANY($2::text[])
			AND NOT (a.approval_gated AND a.due_at < $1 - interval '5 minutes')
		ORDER BY a.due_at, a.id LIMIT 1`, s.now(), req.GuildIDs).Scan(&candidate, &guildID, &channelID)
	if errors.Is(err, pgx.ErrNoRows) {
		commitEmpty()
		return
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	q := s.queries.WithTx(tx)
	if err := q.LockDiscordChannel(c.Request.Context(), guildID+":"+channelID); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	var id pgtype.UUID
	err = tx.QueryRow(c.Request.Context(), `
		SELECT a.id FROM announcements a JOIN discord_channel_bindings b
		ON b.instance_id = a.instance_id AND b.guild_id = a.guild_id AND b.channel_id = a.channel_id
		WHERE a.id = $1 AND a.status = 'pending' AND a.due_at <= $2 AND NOT (a.approval_gated AND a.due_at < $2 - interval '5 minutes')
		FOR UPDATE OF a SKIP LOCKED`, candidate, s.now()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		commitEmpty()
		return
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	// An automated scores post whose standings moved since it was drafted (and approved), or whose game still
	// has a placeholder for its results, must not send: hold it again quietly; automation redrafts it and re-asks.
	current, err := s.scoresStillCurrent(c.Request.Context(), tx, id)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	var awaiting bool
	if err := tx.QueryRow(c.Request.Context(), `SELECT awaiting_results FROM announcements WHERE id = $1`, id).Scan(&awaiting); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if !current || awaiting {
		if _, err := tx.Exec(c.Request.Context(), `UPDATE announcements SET status = 'draft', scheduled_at = NULL, approval_notified_at = $2 WHERE id = $1`, id, s.now()); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		commitEmpty()
		return
	}
	a, err := scanAnnouncement(tx.QueryRow(c.Request.Context(), `
		UPDATE announcements a SET status = 'sending', claimed_at = $2
		FROM instances i WHERE a.id = $1 AND i.id = a.instance_id
		RETURNING `+announcementColumns, id, s.now()))
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"announcement": a})
}

func (s *Server) listAnnouncements(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return
	}
	if err := requireWordleInstanceAdmin(c.Request.Context(), s.queries, toPGUUID(instanceID), c.Request); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	rows, err := s.pool.Query(c.Request.Context(), `SELECT `+announcementColumns+` FROM announcements a JOIN instances i ON i.id = a.instance_id WHERE i.public_id = $1 ORDER BY a.created_at, a.id`, toPGUUID(instanceID))
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rows.Close()
	result := []announcementRecord{}
	for rows.Next() {
		a, err := scanAnnouncement(rows)
		if err != nil {
			writeAnnouncementError(c, err)
			return
		}
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"announcements": result})
}

func (s *Server) finishAnnouncement(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	id, err := uuid.Parse(c.Param("announcementID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid announcement id"})
		return
	}
	var req struct {
		MessageID string `json:"message_id"`
		Failed    bool   `json:"failed"`
		// ThreadID is the thread the bot opened for this announcement, reported even if the post then failed.
		ThreadID string `json:"thread_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Failed && req.MessageID != "") || (!req.Failed && !validAnnouncementID(req.MessageID)) || (req.ThreadID != "" && !validAnnouncementID(req.ThreadID)) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid delivery result"})
		return
	}
	status := "sent"
	if req.Failed {
		status = "failed"
	}
	if req.ThreadID != "" {
		if _, err := s.pool.Exec(c.Request.Context(), `
			INSERT INTO announcement_threads (instance_id, channel_id, thread_key, thread_id)
			SELECT instance_id, channel_id, thread->>'key', $2 FROM announcements WHERE id = $1 AND status = 'sending' AND thread IS NOT NULL
			ON CONFLICT (instance_id, channel_id, thread_key) DO UPDATE SET thread_id = EXCLUDED.thread_id`, toPGUUID(id), req.ThreadID); err != nil {
			writeAnnouncementError(c, err)
			return
		}
	}
	result, err := s.pool.Exec(c.Request.Context(), `
		UPDATE announcements SET status = $2, message_id = NULLIF($3, ''),
		sent_at = CASE WHEN status = 'sent' THEN sent_at WHEN $2 = 'sent' THEN $4 ELSE NULL END
		WHERE id = $1 AND (status = 'sending' OR (status = $2 AND message_id IS NOT DISTINCT FROM NULLIF($3, '')))`, toPGUUID(id), status, req.MessageID, s.now())
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if result.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, errorResponse{Error: "announcement is not awaiting delivery"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": status, "message_id": req.MessageID})
}

func writeAnnouncementError(c *gin.Context, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "instance or announcement not found"})
		return
	}
	var failure *progressionFailure
	if errors.As(err, &failure) {
		c.JSON(failure.status, errorResponse{Error: failure.message})
		return
	}
	c.JSON(statusFromPg(err), errorResponse{Error: err.Error()})
}

// scheduleAnnouncement queues a draft or pending announcement for scheduled_at, or now when omitted.
// The bot reads the text when it sends, so later edits still apply.
func (s *Server) scheduleAnnouncement(c *gin.Context) {
	var req struct {
		ScheduledAt *time.Time `json:"scheduled_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid schedule"})
		return
	}
	var at *time.Time
	due := s.now()
	if req.ScheduledAt != nil {
		v := req.ScheduledAt.UTC().Truncate(time.Microsecond)
		if v.Before(s.now()) {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "scheduled_at must be in the future"})
			return
		}
		at, due = &v, v
	}
	// A draft may have outlived its channel binding; the bot only sends to bound channels, so refuse rather than queue forever.
	s.changeUnsentAnnouncement(c, `UPDATE announcements SET status = 'pending', scheduled_at = $3, due_at = $4`,
		` AND NOT approval_gated AND EXISTS (SELECT 1 FROM discord_channel_bindings b WHERE b.instance_id = announcements.instance_id AND b.guild_id = announcements.guild_id AND b.channel_id = announcements.channel_id)`,
		"no unsent, ungated announcement with that id whose channel is still bound to this instance (approval-gated posts are scheduled by approving them)", at, due)
}

// unscheduleAnnouncement turns a scheduled announcement back into a draft.
func (s *Server) unscheduleAnnouncement(c *gin.Context) {
	s.changeUnsentAnnouncement(c, `UPDATE announcements SET status = 'draft', scheduled_at = NULL, approval_claimed_at = NULL, approval_notified_at = NULL`, "", "")
}

// editAnnouncement replaces the text of an announcement that hasn't been sent.
func (s *Server) editAnnouncement(c *gin.Context) {
	var req struct {
		Body string `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || utf8.RuneCountInString(req.Body) > 2000 || strings.TrimSpace(req.Body) == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "body must be nonblank and at most 2000 characters"})
		return
	}
	// A gated draft whose text changes is re-sent to admins for approval of the new copy.
	s.changeUnsentAnnouncement(c, `UPDATE announcements SET body = $3, approval_claimed_at = NULL, approval_notified_at = NULL,
		status = CASE WHEN approval_gated THEN 'draft' ELSE status END,
		scheduled_at = CASE WHEN approval_gated THEN NULL ELSE scheduled_at END`, "", "", req.Body)
}

// markAnnouncementSent records an announcement the operator posted some other way, so the bot never sends it.
func (s *Server) markAnnouncementSent(c *gin.Context) {
	var req struct {
		MessageID string `json:"message_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.MessageID != "" && !validAnnouncementID(req.MessageID)) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "message_id must be a Discord message ID when given"})
		return
	}
	s.changeUnsentAnnouncement(c, `UPDATE announcements SET status = 'sent', sent_at = $4, message_id = NULLIF($3, '')`, "", "", req.MessageID, s.now())
}

// deleteAnnouncement removes an announcement that hasn't been sent.
func (s *Server) deleteAnnouncement(c *gin.Context) {
	s.changeUnsentAnnouncement(c, `DELETE FROM announcements`, "", "")
}

// changeUnsentAnnouncement runs statement (with $1 = announcement, $2 = instance, $3... = args) against a
// draft or pending announcement. Once the bot has claimed it (sending) the row no longer matches.
func (s *Server) changeUnsentAnnouncement(c *gin.Context, statement, extraWhere, conflict string, args ...any) {
	if !requireAdminService(c) {
		return
	}
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("announcementID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid announcement id"})
		return
	}
	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	q := s.queries.WithTx(tx)
	if _, err := q.LockInstanceForProgression(c.Request.Context(), toPGUUID(instanceID)); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if err := requireWordleInstanceAdmin(c.Request.Context(), q, toPGUUID(instanceID), c.Request); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	result, err := tx.Exec(c.Request.Context(), statement+` WHERE id = $1 AND instance_id = (SELECT id FROM instances WHERE public_id = $2) AND status IN ('draft', 'pending')`+extraWhere,
		append([]any{toPGUUID(id), toPGUUID(instanceID)}, args...)...)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if result.RowsAffected() == 0 {
		if conflict == "" {
			conflict = "no unsent announcement with that id in this instance (already sending or sent?)"
		}
		c.JSON(http.StatusConflict, errorResponse{Error: conflict})
		return
	}
	if strings.HasPrefix(statement, "DELETE") {
		if err := tx.Commit(c.Request.Context()); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"deleted": id})
		return
	}
	a, err := scanAnnouncement(tx.QueryRow(c.Request.Context(), `SELECT `+announcementColumns+` FROM announcements a JOIN instances i ON i.id = a.instance_id WHERE a.id = $1`, toPGUUID(id)))
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"announcement": a})
}
