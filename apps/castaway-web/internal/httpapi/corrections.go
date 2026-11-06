package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// createScoreCorrection adds an admin's correction to a player's points on an existing occurrence (a game,
// a challenge), so the change sits in that week's scores with a reason instead of rewriting the award.
func (s *Server) createScoreCorrection(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok || !s.requireInstanceAdminRequest(c, instanceID) {
		return
	}
	occurrenceID, ok := parseUUIDPath(c, "occurrenceID")
	if !ok {
		return
	}
	var req struct {
		ParticipantID string `json:"participant_id"`
		Points        int    `json:"points"`
		Reason        string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Points == 0 || req.Points < -50 || req.Points > 50 || strings.TrimSpace(req.Reason) == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "need participant_id, non-zero points (±50) and a reason"})
		return
	}
	var entryID string
	err := s.pool.QueryRow(c.Request.Context(), `INSERT INTO bonus_point_ledger_entries
			(instance_id, participant_id, activity_occurrence_id, entry_kind, points, reason, effective_at, award_key)
		SELECT i.id, p.id, o.id, 'correction', $4, $5, o.effective_at, 'correction:' || gen_random_uuid()
		FROM instances i
		JOIN participants p ON p.instance_id = i.id AND p.public_id::text = $3
		JOIN activity_occurrences o ON o.public_id = $2
		JOIN instance_activities a ON a.id = o.activity_id AND a.instance_id = i.id
		WHERE i.public_id = $1
		RETURNING public_id::text`, instanceID, occurrenceID, req.ParticipantID, req.Points, strings.TrimSpace(req.Reason)).Scan(&entryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, errorResponse{Error: "player or occurrence not found in this instance"})
			return
		}
		c.JSON(http.StatusInternalServerError, errorResponse{Error: "could not save the correction"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": entryID})
}
