package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// Access requests come from signed-in Discord users who aren't linked to a player. The bot DMs the admin
// contact about each once (claim), and admins list or dismiss them; approving is linking a player.

func (s *Server) claimAccessRequest(c *gin.Context) {
	var id, username string
	var at time.Time
	err := s.pool.QueryRow(c.Request.Context(), `
		UPDATE access_requests SET notified_at = $1
		WHERE discord_user_id = (SELECT discord_user_id FROM access_requests WHERE notified_at IS NULL ORDER BY requested_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING discord_user_id, discord_username, requested_at`, s.now()).Scan(&id, &username, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusOK, gin.H{"access_request": nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"access_request": gin.H{"discord_user_id": id, "discord_username": username, "requested_at": at.UTC().Format(time.RFC3339)}})
}

func (s *Server) requireAnyAdmin(c *gin.Context) bool {
	ok, err := s.isAnyInstanceAdmin(c.Request.Context(), discordUserIDFromRequest(c.Request))
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return false
	}
	if !ok {
		c.JSON(http.StatusForbidden, errorResponse{Error: "forbidden"})
	}
	return ok
}

// listAccessRequests shows requests from users who still aren't linked to any player.
func (s *Server) listAccessRequests(c *gin.Context) {
	if !s.requireAnyAdmin(c) {
		return
	}
	rows, err := s.pool.Query(c.Request.Context(), `
		SELECT discord_user_id, discord_username, requested_at FROM access_requests a
		WHERE NOT EXISTS (SELECT 1 FROM participants p WHERE p.discord_user_id = a.discord_user_id)
		ORDER BY requested_at`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	defer rows.Close()
	out := []gin.H{}
	for rows.Next() {
		var id, username string
		var at time.Time
		if err := rows.Scan(&id, &username, &at); err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		out = append(out, gin.H{"discord_user_id": id, "discord_username": username, "requested_at": at.UTC().Format(time.RFC3339)})
	}
	c.JSON(http.StatusOK, gin.H{"access_requests": out})
}

func (s *Server) deleteAccessRequest(c *gin.Context) {
	if !s.requireAnyAdmin(c) {
		return
	}
	if _, err := s.pool.Exec(c.Request.Context(), `DELETE FROM access_requests WHERE discord_user_id = $1`, c.Param("discordUserID")); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// addInstanceViewer lets a Discord user view the instance's site without playing, and clears their access request.
func (s *Server) addInstanceViewer(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok || !s.requireInstanceAdminRequest(c, instanceID) {
		return
	}
	var req struct {
		DiscordUserID string `json:"discord_user_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	_, err := s.pool.Exec(c.Request.Context(), `WITH v AS (
		INSERT INTO instance_viewers (instance_id, discord_user_id) SELECT id, $2 FROM instances WHERE public_id = $1 ON CONFLICT DO NOTHING
	) DELETE FROM access_requests WHERE discord_user_id = $2`, toPGUUID(instanceID), req.DiscordUserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"instance_id": instanceID.String(), "discord_user_id": req.DiscordUserID})
}

// addInstanceAdmin lets an existing instance admin make another Discord user an admin of that instance.
func (s *Server) addInstanceAdmin(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok || !s.requireInstanceAdminRequest(c, instanceID) {
		return
	}
	var req struct {
		DiscordUserID string `json:"discord_user_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if _, err := s.pool.Exec(c.Request.Context(), `INSERT INTO instance_admins (instance_id, discord_user_id) SELECT id, $2 FROM instances WHERE public_id = $1 ON CONFLICT DO NOTHING`, toPGUUID(instanceID), req.DiscordUserID); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"instance_id": instanceID.String(), "discord_user_id": req.DiscordUserID})
}
