package httpapi

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func WithBootstrapAdminDiscordUserID(id string) Option {
	return func(s *Server) { s.bootstrapAdminDiscordUserID = strings.TrimSpace(id) }
}

func requireAdminService(c *gin.Context) bool {
	if _, ok := ServicePrincipal(c.Request.Context()); !ok {
		c.JSON(http.StatusUnauthorized, errorResponse{Error: "service authentication required"})
		return false
	}
	return true
}

func (s *Server) adminSession(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	principal, _ := ServicePrincipal(c.Request.Context())
	authentication := "trusted-service delegation; Discord identity asserted by service"
	if principal == webSessionActor {
		authentication = "Discord login session"
	}
	c.JSON(http.StatusOK, gin.H{"principal": principal, "discord_user_id": discordUserIDFromRequest(c.Request), "authentication": authentication})
}

func (s *Server) bootstrapInstanceAdmin(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	actor := discordUserIDFromRequest(c.Request)
	if s.bootstrapAdminDiscordUserID == "" || actor != s.bootstrapAdminDiscordUserID {
		c.JSON(http.StatusForbidden, errorResponse{Error: "admin bootstrap is not authorized"})
		return
	}
	id, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return
	}
	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		writeWordleError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	q := s.queries.WithTx(tx)
	if _, err := q.LockInstanceForProgression(c.Request.Context(), toPGUUID(id)); err != nil {
		writeWordleError(c, err)
		return
	}
	admin, err := q.IsInstanceAdmin(c.Request.Context(), db.IsInstanceAdminParams{InstanceID: toPGUUID(id), DiscordUserID: actor})
	if err != nil {
		writeWordleError(c, err)
		return
	}
	if !admin {
		count, err := q.CountInstanceAdmins(c.Request.Context(), toPGUUID(id))
		if err != nil {
			writeWordleError(c, err)
			return
		}
		if count != 0 {
			c.JSON(http.StatusConflict, errorResponse{Error: "instance already has an admin"})
			return
		}
		if _, err := q.CreateInstanceAdmin(c.Request.Context(), db.CreateInstanceAdminParams{InstanceID: toPGUUID(id), DiscordUserID: actor}); err != nil {
			writeWordleError(c, err)
			return
		}
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		writeWordleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"instance_id": id.String(), "discord_user_id": actor, "admin": true})
}

func discordChannelParams(c *gin.Context) (db.GetDiscordChannelBindingParams, bool) {
	p := db.GetDiscordChannelBindingParams{GuildID: c.Param("guildID"), ChannelID: c.Param("channelID")}
	for _, id := range []string{p.GuildID, p.ChannelID} {
		n, err := strconv.ParseUint(id, 10, 64)
		if err != nil || n == 0 {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "guild and channel IDs must be Discord snowflakes"})
			return p, false
		}
	}
	return p, true
}

func (s *Server) getDiscordChannelBinding(c *gin.Context) {
	if !requireAdminService(c) {
		return
	}
	p, ok := discordChannelParams(c)
	if !ok {
		return
	}
	row, err := s.queries.GetDiscordChannelBinding(c.Request.Context(), p)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "channel is not bound"})
		return
	}
	if err != nil {
		writeWordleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"binding": gin.H{"guild_id": row.GuildID, "channel_id": row.ChannelID, "instance_id": pgUUIDString(row.InstanceID)}})
}

// listDiscordChannelBindings lists the channels bound to an instance (its season channels), for the bot-permission check.
func (s *Server) listDiscordChannelBindings(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok || !s.requireInstanceAdminRequest(c, instanceID) {
		return
	}
	rows, err := s.pool.Query(c.Request.Context(), `SELECT b.guild_id, b.channel_id FROM discord_channel_bindings b
		JOIN instances i ON i.id = b.instance_id WHERE i.public_id = $1 ORDER BY b.guild_id, b.channel_id`, toPGUUID(instanceID))
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rows.Close()
	channels := []gin.H{}
	for rows.Next() {
		var guildID, channelID string
		if err := rows.Scan(&guildID, &channelID); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		channels = append(channels, gin.H{"guild_id": guildID, "channel_id": channelID})
	}
	if err := rows.Err(); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"channels": channels})
}

func (s *Server) setDiscordChannelBinding(c *gin.Context)    { s.changeDiscordChannelBinding(c, false) }
func (s *Server) deleteDiscordChannelBinding(c *gin.Context) { s.changeDiscordChannelBinding(c, true) }

func (s *Server) changeDiscordChannelBinding(c *gin.Context, remove bool) {
	if !requireAdminService(c) {
		return
	}
	p, ok := discordChannelParams(c)
	if !ok {
		return
	}
	var body struct {
		InstanceID string `json:"instance_id"`
		Replace    bool   `json:"replace"`
	}
	var target uuid.UUID
	if !remove {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid binding request"})
			return
		}
		var err error
		target, err = uuid.Parse(body.InstanceID)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid instance_id"})
			return
		}
	}
	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		writeWordleError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	q := s.queries.WithTx(tx)
	if err := q.LockDiscordChannel(c.Request.Context(), p.GuildID+":"+p.ChannelID); err != nil {
		writeWordleError(c, err)
		return
	}
	old, err := q.GetDiscordChannelBinding(c.Request.Context(), p)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeWordleError(c, err)
		return
	}
	if remove && !exists {
		c.JSON(http.StatusNotFound, errorResponse{Error: "channel is not bound"})
		return
	}
	ids := []string{}
	if exists {
		ids = append(ids, pgUUIDString(old.InstanceID))
	}
	if !remove && (!exists || old.InstanceID != toPGUUID(target)) {
		ids = append(ids, target.String())
	}
	sort.Strings(ids)
	for _, id := range ids {
		instance := toPGUUID(uuid.MustParse(id))
		if _, err := q.LockInstanceForProgression(c.Request.Context(), instance); err != nil {
			writeWordleError(c, err)
			return
		}
		if err := requireWordleInstanceAdmin(c.Request.Context(), q, instance, c.Request); err != nil {
			writeWordleError(c, err)
			return
		}
	}
	if !remove && exists && old.InstanceID != toPGUUID(target) && !body.Replace {
		c.JSON(http.StatusConflict, errorResponse{Error: "channel is already bound; confirm replacement"})
		return
	}
	if exists && (remove || old.InstanceID != toPGUUID(target)) {
		var undelivered bool
		if err := tx.QueryRow(c.Request.Context(), `SELECT EXISTS (
			SELECT 1 FROM announcements WHERE guild_id = $1 AND channel_id = $2 AND status IN ('pending', 'sending')
		)`, p.GuildID, p.ChannelID).Scan(&undelivered); err != nil {
			writeWordleError(c, err)
			return
		}
		if undelivered {
			c.JSON(http.StatusConflict, errorResponse{Error: "channel has an undelivered announcement; resolve it before rebinding"})
			return
		}
	}
	if remove {
		err = q.DeleteDiscordChannelBinding(c.Request.Context(), db.DeleteDiscordChannelBindingParams(p))
	} else {
		err = q.SetDiscordChannelBinding(c.Request.Context(), db.SetDiscordChannelBindingParams{GuildID: p.GuildID, ChannelID: p.ChannelID, InstanceID: toPGUUID(target)})
	}
	if err != nil {
		writeWordleError(c, err)
		return
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		writeWordleError(c, err)
		return
	}
	if remove {
		c.JSON(http.StatusOK, gin.H{"unbound": true})
		return
	}
	c.JSON(http.StatusOK, gin.H{"binding": gin.H{"guild_id": p.GuildID, "channel_id": p.ChannelID, "instance_id": target.String()}})
}

func (s *Server) changeDiscordPlayerLink(c *gin.Context, remove bool) {
	if !requireAdminService(c) {
		return
	}
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return
	}
	participantID, ok := parseUUIDPath(c, "participantID")
	if !ok {
		return
	}
	actor := discordUserIDFromRequest(c.Request)
	target := actor
	if value := strings.TrimSpace(c.Query("discord_user_id")); value != "" {
		target = value
	}
	if !remove && c.Request.ContentLength != 0 {
		var req linkParticipantDiscordUserRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid player link"})
			return
		}
		if strings.TrimSpace(req.DiscordUserID) != "" {
			target = strings.TrimSpace(req.DiscordUserID)
		}
	}
	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		writeWordleError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	q := s.queries.WithTx(tx)
	if _, err := q.LockInstanceForProgression(c.Request.Context(), toPGUUID(instanceID)); err != nil {
		writeWordleError(c, err)
		return
	}
	if err := requireWordleInstanceAdmin(c.Request.Context(), q, toPGUUID(instanceID), c.Request); err != nil {
		writeWordleError(c, err)
		return
	}
	participant, err := q.GetParticipant(c.Request.Context(), toPGUUID(participantID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && participant.InstanceID != toPGUUID(instanceID)) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "participant not found"})
		return
	}
	if err != nil {
		writeWordleError(c, err)
		return
	}
	if !remove && participant.DiscordUserID.Valid && participant.DiscordUserID.String != target {
		c.JSON(http.StatusConflict, errorResponse{Error: "player is already linked; unlink before assigning another user"})
		return
	}
	if remove {
		_, err = q.ClearParticipantDiscordUserID(c.Request.Context(), toPGUUID(participantID))
	} else {
		_, err = q.SetParticipantDiscordUserID(c.Request.Context(), db.SetParticipantDiscordUserIDParams{ID: toPGUUID(participantID), DiscordUserID: pgtype.Text{String: target, Valid: true}})
	}
	if err != nil {
		writeWordleError(c, err)
		return
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		writeWordleError(c, err)
		return
	}
	if remove {
		target = ""
	}
	c.JSON(http.StatusOK, gin.H{"participant": participantSummaryToJSON(participant.ID, participant.Name, target)})
}
