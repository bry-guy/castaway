package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// raiseAdminAlert queues a DM to the season's admins once per key; raising it again changes nothing.
func (s *Server) raiseAdminAlert(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return
	}
	key := strings.TrimSpace(c.Param("alertKey"))
	var req struct {
		Body   string       `json:"body"`
		Action *alertAction `json:"action"`
	}
	if c.ShouldBindJSON(&req) != nil || key == "" || len(key) > 120 || strings.TrimSpace(req.Body) == "" || utf8.RuneCountInString(req.Body) > 1900 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "an alert key (at most 120 characters) and a nonblank body (at most 1900 characters) are required"})
		return
	}
	var kind, revision *string
	var payload []byte
	if req.Action != nil {
		imp, err := req.Action.episodeImport()
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		rev := imp.fingerprint()[:32]
		kind, revision, payload = &req.Action.Kind, &rev, req.Action.Payload
	}
	ctx := c.Request.Context()
	if err := requireWordleInstanceAdmin(ctx, s.queries, toPGUUID(instanceID), c.Request); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	// A changed action replaces an unapproved one and is DMed afresh; its new revision voids an old "yes".
	tag, err := s.pool.Exec(ctx, `INSERT INTO admin_alerts (instance_id, alert_key, body, action_kind, action, revision)
		SELECT id, $2, $3, $4, $5, $6 FROM instances WHERE public_id = $1
		ON CONFLICT (instance_id, alert_key) DO UPDATE SET body = EXCLUDED.body, action_kind = EXCLUDED.action_kind,
			action = EXCLUDED.action, revision = EXCLUDED.revision, claimed_at = NULL, delivered_at = NULL, created_at = NOW()
		WHERE admin_alerts.applied_at IS NULL AND admin_alerts.revision IS DISTINCT FROM EXCLUDED.revision`,
		toPGUUID(instanceID), key, req.Body, kind, payload, revision)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"created": tag.RowsAffected() == 1})
}

// claimAdminAlert hands the bot the next undelivered alert for a season bound in its guilds, with that
// season's admins. The claim is a 10-minute lease: until delivery is confirmed it's offered again.
func (s *Server) claimAdminAlert(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	var req struct {
		GuildIDs []string `json:"guild_ids"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.GuildIDs) == 0 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "guild_ids are required"})
		return
	}
	if err := s.raiseStaleAutomationAlerts(c.Request.Context()); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	var id uuid.UUID
	var instanceID uuid.UUID
	var body string
	var revision *string
	err := s.pool.QueryRow(c.Request.Context(), `
		UPDATE admin_alerts a SET claimed_at = $1 FROM instances i
		WHERE i.id = a.instance_id AND a.id = (SELECT al.id FROM admin_alerts al
			WHERE al.delivered_at IS NULL AND (al.claimed_at IS NULL OR al.claimed_at < $1::timestamptz - interval '10 minutes')
				AND EXISTS (SELECT 1 FROM discord_channel_bindings b WHERE b.instance_id = al.instance_id AND b.guild_id = ANY($2::text[]))
			ORDER BY al.created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING a.id, i.public_id, a.body, a.revision`, s.now(), req.GuildIDs).Scan(&id, &instanceID, &body, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusOK, gin.H{"alert": nil})
		return
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	admins, err := s.instanceAdminIDs(c, toPGUUID(instanceID))
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"alert": gin.H{"id": id, "body": body, "revision": revision}, "admin_discord_user_ids": admins})
}

// confirmAdminAlert records that every admin got the alert.
func (s *Server) confirmAdminAlert(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	id, err := uuid.Parse(c.Param("alertID"))
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid alert id"})
		return
	}
	if _, err := s.pool.Exec(c.Request.Context(), `UPDATE admin_alerts SET delivered_at = $2 WHERE id = $1 AND delivered_at IS NULL`, id, s.now()); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// alertAction is something admins approve by replying "yes" to an alert.
type alertAction struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

func (a alertAction) episodeImport() (episodeImportRequest, error) {
	var req episodeImportRequest
	if a.Kind != "episode_import" {
		return req, errors.New("action kind must be episode_import")
	}
	if err := json.Unmarshal(a.Payload, &req); err != nil {
		return req, fmt.Errorf("episode_import payload: %w", err)
	}
	return req, req.validate()
}

// approveAdminAction runs an alert's action for the approving admin, if the revision they saw is still
// current. The alert row is locked with the instance, so two approvals can't both apply it.
func (s *Server) approveAdminAction(c *gin.Context) {
	id, err := uuid.Parse(c.Param("alertID"))
	var req struct {
		Revision string `json:"revision"`
	}
	if err != nil || c.ShouldBindJSON(&req) != nil || req.Revision == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "an alert id and revision are required"})
		return
	}
	ctx := c.Request.Context()
	var instanceID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT i.public_id FROM admin_alerts a JOIN instances i ON i.id = a.instance_id WHERE a.id = $1`, id).Scan(&instanceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, errorResponse{Error: "no such alert"})
			return
		}
		writeAnnouncementError(c, err)
		return
	}
	tx, qtx, ok := s.lockLegacyInstanceForAdmin(c, instanceID)
	if !ok {
		return
	}
	defer rollbackTx(c, tx)
	var action alertAction
	var revision *string
	var applied *time.Time
	if err := tx.QueryRow(ctx, `SELECT COALESCE(action_kind, ''), COALESCE(action, 'null'::jsonb), revision, applied_at FROM admin_alerts WHERE id = $1 FOR UPDATE`, id).
		Scan(&action.Kind, &action.Payload, &revision, &applied); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	switch {
	case revision == nil:
		c.JSON(http.StatusConflict, errorResponse{Error: "this alert has nothing to approve"})
		return
	case applied != nil:
		c.JSON(http.StatusConflict, errorResponse{Error: "already approved and recorded"})
		return
	case *revision != req.Revision:
		c.JSON(http.StatusConflict, errorResponse{Error: "there's a newer version of this; reply to the latest DM instead"})
		return
	}
	imp, err := action.episodeImport()
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	res, err := s.applyEpisodeImport(ctx, tx, qtx, instanceID, imp)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE admin_alerts SET applied_at = $2, applied_by = $3 WHERE id = $1`, id, s.now(), c.GetHeader(discordUserIDHeader))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeTribeError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// listScheduledGames lists a season's scheduled (episode) games of every type, without answers, so
// automation can check each week has its game.
func (s *Server) listScheduledGames(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := requireWordleInstanceAdmin(ctx, s.queries, toPGUUID(instanceID), c.Request); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	rows, err := s.pool.Query(ctx, `
		SELECT g.type, g.public_id, g.episode_number, g.opens_at, g.cutoff_at, g.scored FROM (
			SELECT 'castawordle' AS type, w.public_id, w.episode_number, w.opens_at, w.cutoff_at, w.instance_id,
				COALESCE(o.status = 'resolved', w.wordle_round_id IS NULL) AS scored
				FROM castawordle_games w LEFT JOIN activity_occurrences o ON o.public_id = w.wordle_round_id WHERE w.episode_number IS NOT NULL
			UNION ALL SELECT 'press_the_button', public_id, episode_number, opens_at, cutoff_at, instance_id, resolved_at IS NOT NULL FROM button_games WHERE episode_number IS NOT NULL
			UNION ALL SELECT 'spell_it_out', public_id, episode_number, opens_at, cutoff_at, instance_id, resolved_at IS NOT NULL FROM scramble_games WHERE episode_number IS NOT NULL
			UNION ALL SELECT 'inside_track', public_id, episode_number, opens_at, cutoff_at, instance_id, resolved_at IS NOT NULL FROM track_games
			UNION ALL SELECT 'free_the_block', public_id, episode_number, opens_at, cutoff_at, instance_id, resolved_at IS NOT NULL FROM free_the_block_games
		) g JOIN instances i ON i.id = g.instance_id WHERE i.public_id = $1 ORDER BY g.episode_number, g.type`, toPGUUID(instanceID))
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rows.Close()
	games := []gin.H{}
	for rows.Next() {
		var kind string
		var id uuid.UUID
		var episode int32
		var opens, cutoff time.Time
		var scored bool
		if err := rows.Scan(&kind, &id, &episode, &opens, &cutoff, &scored); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		games = append(games, gin.H{"type": kind, "id": id, "episode_number": episode, "opens_at": opens.UTC(), "cutoff_at": cutoff.UTC(), "scored": scored})
	}
	if err := rows.Err(); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"games": games})
}
