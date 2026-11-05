package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ── Episode imports ─────────────────────────────────────────────────────────────────────────────────

type episodeImportBoot struct {
	Position     int    `json:"position"`
	ContestantID string `json:"contestant_id"`
}

type episodeImportChallenge struct {
	Key           string    `json:"key"`
	LegacyKeys    []string  `json:"legacy_keys"`
	Kind          string    `json:"kind"`
	WinningTribes []string  `json:"winning_tribes"`
	EffectiveAt   time.Time `json:"effective_at"`
}

type episodeImportRequest struct {
	EpisodeNumber  int    `json:"episode_number"`
	Source         string `json:"source"`
	SourceRevision string `json:"source_revision"`
	// Replace lets a survivoR import replace an episode first scored from the wiki.
	Replace    bool                     `json:"replace"`
	Boots      []episodeImportBoot      `json:"boots"`
	Challenges []episodeImportChallenge `json:"challenges"`
	// ChampionChallenges are post-merge challenges won by castaways (contestant public IDs).
	ChampionChallenges []championImportChallenge `json:"champion_challenges"`
}

type championImportChallenge struct {
	Key         string    `json:"key"`
	Kind        string    `json:"kind"`
	Winners     []string  `json:"winners"`
	EffectiveAt time.Time `json:"effective_at"`
}

// fingerprint identifies the results (not where they came from), so a newer upstream revision with the
// same results replays as a no-op.
func (r episodeImportRequest) fingerprint() string {
	boots := make([]string, 0, len(r.Boots))
	for _, b := range r.Boots {
		boots = append(boots, fmt.Sprintf("%d=%s", b.Position, strings.ToLower(b.ContestantID)))
	}
	challenges := make([]string, 0, len(r.Challenges))
	for _, ch := range r.Challenges {
		tribes := append([]string(nil), ch.WinningTribes...)
		for i := range tribes {
			tribes[i] = strings.ToLower(strings.TrimSpace(tribes[i]))
		}
		sort.Strings(tribes)
		challenges = append(challenges, ch.Key+"|"+strings.ToLower(ch.Kind)+"|"+strings.Join(tribes, ",")+"|"+ch.EffectiveAt.UTC().Format(time.RFC3339))
	}
	for _, ch := range r.ChampionChallenges { // absent before the merge, so earlier fingerprints are unchanged
		winners := append([]string(nil), ch.Winners...)
		sort.Strings(winners)
		challenges = append(challenges, "champion:"+ch.Key+"|"+strings.ToLower(ch.Kind)+"|"+strings.ToLower(strings.Join(winners, ","))+"|"+ch.EffectiveAt.UTC().Format(time.RFC3339))
	}
	sort.Strings(boots)
	sort.Strings(challenges)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\n%s\n%s", r.EpisodeNumber, strings.Join(boots, ";"), strings.Join(challenges, ";"))))
	return hex.EncodeToString(sum[:])
}

// importEpisode records an episode's boots and tribe challenge wins all at once: everything is checked
// first and applied in one transaction under the instance lock, or nothing is. Already-recorded matching
// results are reused; anything that disagrees with what's recorded is a 409 for an admin to sort out.
func (s *Server) importEpisode(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return
	}
	var req episodeImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if err := req.validate(); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	tx, qtx, ok := s.lockLegacyInstanceForAdmin(c, instanceID)
	if !ok {
		return
	}
	defer rollbackTx(c, tx)
	ctx := c.Request.Context()
	res, err := s.applyEpisodeImport(ctx, tx, qtx, instanceID, req)
	if err == nil && (res["status"] == "applied" || res["status"] == "replaced") {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeTribeError(c, err)
		return
	}
	code := http.StatusOK
	if res["status"] == "applied" {
		code = http.StatusCreated
	}
	c.JSON(code, res)
}

func (r episodeImportRequest) validate() error {
	if r.EpisodeNumber < 0 || (r.Source != "survivor" && r.Source != "manual" && r.Source != "wiki") || (r.Replace && r.Source != "survivor") || len(r.SourceRevision) > 80 {
		return errors.New("episode_number (0+) and source (survivor, manual or wiki) are required; only survivor can replace")
	}
	for _, ch := range r.Challenges {
		if ch.Key == "" || len(ch.Key) > 64 || ch.EffectiveAt.IsZero() || len(ch.WinningTribes) == 0 || (ch.Kind != "immunity" && ch.Kind != "reward") {
			return errors.New("each challenge needs a key (1-64 characters), kind immunity or reward, winning_tribes and effective_at")
		}
	}
	for _, ch := range r.ChampionChallenges {
		if ch.Key == "" || len(ch.Key) > 64 || ch.EffectiveAt.IsZero() || len(ch.Winners) == 0 || (ch.Kind != "immunity" && ch.Kind != "reward") {
			return errors.New("each champion challenge needs a key (1-64 characters), kind immunity or reward, winners and effective_at")
		}
	}
	return nil
}

// applyEpisodeImport records an episode's results inside tx, which must hold the instance lock; the caller
// commits. Replaying the same results is "unchanged"; different results for an imported episode conflict.
func (s *Server) applyEpisodeImport(ctx context.Context, tx pgx.Tx, qtx *db.Queries, instanceID uuid.UUID, req episodeImportRequest) (gin.H, error) {
	fingerprint := req.fingerprint()
	var stored, storedSource string
	var storedBoots []int32
	var storedKeys []string
	err := tx.QueryRow(ctx, `SELECT e.fingerprint, e.source, e.boot_positions, e.challenge_keys FROM episode_imports e JOIN instances i ON i.id = e.instance_id
		WHERE i.public_id = $1 AND e.episode_number = $2`, toPGUUID(instanceID), req.EpisodeNumber).Scan(&stored, &storedSource, &storedBoots, &storedKeys)
	switch {
	case err == nil && stored == fingerprint:
		return gin.H{"status": "unchanged"}, nil
	case err == nil && req.Replace && storedSource == "wiki":
		return s.replaceWikiImport(ctx, tx, qtx, instanceID, req, storedBoots, storedKeys)
	case err == nil:
		return nil, progressionError(http.StatusConflict, fmt.Sprintf("episode %d was already imported with different results; an admin needs to review it", req.EpisodeNumber))
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	recorded, err := s.applyImportBoots(ctx, qtx, instanceID, req.Boots)
	if err != nil {
		return nil, err
	}
	awarded := 0
	keys := make([]string, 0, len(req.Challenges))
	for _, ch := range req.Challenges {
		_, n, _, err := applyTribeChallenge(ctx, tx, qtx, instanceID, append([]string{ch.Key}, ch.LegacyKeys...), ch.Kind, ch.WinningTribes, ch.EffectiveAt.UTC().Truncate(time.Microsecond))
		if err != nil {
			return nil, err
		}
		awarded += n
		keys = append(keys, ch.Key)
	}
	for _, ch := range req.ChampionChallenges {
		n, err := applyChampionChallenge(ctx, tx, qtx, instanceID, ch.Key, ch.Kind, ch.Winners, ch.EffectiveAt.UTC().Truncate(time.Microsecond))
		if err != nil {
			return nil, err
		}
		awarded += n
		keys = append(keys, ch.Key)
	}
	positions := make([]int32, 0, len(req.Boots))
	for _, b := range req.Boots {
		positions = append(positions, int32(b.Position)) // #nosec G115 -- validated against the roster size
	}
	if _, err := tx.Exec(ctx, `INSERT INTO episode_imports (instance_id, episode_number, source, source_revision, boot_positions, challenge_keys, fingerprint, applied_at)
		SELECT id, $2, $3, $4, $5, $6, $7, $8 FROM instances WHERE public_id = $1`,
		toPGUUID(instanceID), req.EpisodeNumber, req.Source, req.SourceRevision, positions, keys, fingerprint, s.now()); err != nil {
		return nil, err
	}
	return gin.H{"status": "applied", "outcomes_recorded": recorded, "challenge_awards": awarded}, nil
}

// applyImportBoots places each booted contestant, or confirms they're already placed there.
func (s *Server) applyImportBoots(ctx context.Context, qtx *db.Queries, instanceID uuid.UUID, boots []episodeImportBoot) (int, error) {
	outcomes, err := qtx.ListOutcomePositionsByInstance(ctx, toPGUUID(instanceID))
	if err != nil {
		return 0, err
	}
	at := map[int]string{}
	placed := map[string]int{}
	for _, o := range outcomes {
		if o.ContestantID.Valid {
			id := pgUUIDString(o.ContestantID)
			at[int(o.Position)], placed[id] = id, int(o.Position)
		}
	}
	recorded := 0
	for _, b := range boots {
		id, err := uuid.Parse(b.ContestantID)
		if err != nil || b.Position <= 0 || b.Position > 1000 {
			return 0, progressionError(http.StatusBadRequest, "each boot needs a positive position and a contestant_id")
		}
		key := id.String()
		switch {
		case at[b.Position] == key:
			continue
		case at[b.Position] != "":
			return 0, progressionError(http.StatusConflict, fmt.Sprintf("place %d already holds a different contestant", b.Position))
		case placed[key] != 0:
			return 0, progressionError(http.StatusConflict, fmt.Sprintf("that contestant is already recorded at place %d", placed[key]))
		}
		belongs, err := qtx.InstanceHasContestant(ctx, db.InstanceHasContestantParams{InstanceID: toPGUUID(instanceID), ContestantID: toPGUUID(id)})
		if err != nil {
			return 0, err
		}
		if !belongs {
			return 0, progressionError(http.StatusBadRequest, "contestant does not belong to this instance")
		}
		if _, err := qtx.UpsertOutcomePosition(ctx, db.UpsertOutcomePositionParams{InstanceID: toPGUUID(instanceID), Position: int32(b.Position), ContestantID: toPGUUID(id)}); err != nil { // #nosec G115 -- bounded above
			return 0, err
		}
		at[b.Position], placed[key] = key, b.Position
		recorded++
	}
	return recorded, nil
}

func (s *Server) listEpisodeImports(c *gin.Context) {
	instanceID, ok := s.automationInstance(c)
	if !ok {
		return
	}
	rows, err := s.pool.Query(c.Request.Context(), `SELECT e.episode_number, e.source, e.source_revision, e.boot_positions, e.challenge_keys, e.applied_at
		FROM episode_imports e JOIN instances i ON i.id = e.instance_id WHERE i.public_id = $1 ORDER BY e.episode_number`, toPGUUID(instanceID))
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rows.Close()
	imports := []gin.H{}
	for rows.Next() {
		var episode int32
		var source, revision string
		var positions []int32
		var keys []string
		var applied time.Time
		if err := rows.Scan(&episode, &source, &revision, &positions, &keys, &applied); err != nil {
			writeAnnouncementError(c, err)
			return
		}
		imports = append(imports, gin.H{"episode_number": episode, "source": source, "source_revision": revision, "boot_positions": positions, "challenge_keys": keys, "applied_at": applied.UTC()})
	}
	if err := rows.Err(); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"imports": imports})
}

// automationInstance checks an admin service request for the path's instance.
func (s *Server) automationInstance(c *gin.Context) (uuid.UUID, bool) {
	if !requireAdminService(c) {
		return uuid.UUID{}, false
	}
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok {
		return uuid.UUID{}, false
	}
	if err := requireWordleInstanceAdmin(c.Request.Context(), s.queries, toPGUUID(instanceID), c.Request); err != nil {
		writeAnnouncementError(c, err)
		return uuid.UUID{}, false
	}
	return instanceID, true
}

// ── Standings fingerprints and weekly snapshots ─────────────────────────────────────────────────────

// scoreStandings returns the public leaderboard (server order, visible bonuses only) and a fingerprint of
// the points in it; any scoring change changes the fingerprint.
func (s *Server) scoreStandings(ctx context.Context, instanceID uuid.UUID) (json.RawMessage, string, error) {
	rec := httptest.NewRecorder()
	gc, _ := gin.CreateTestContext(rec)
	gc.Request = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	gc.Params = gin.Params{{Key: "instanceID", Value: instanceID.String()}}
	s.leaderboard(gc)
	if rec.Code != http.StatusOK {
		return nil, "", fmt.Errorf("leaderboard HTTP %d", rec.Code)
	}
	var res struct {
		Leaderboard json.RawMessage `json:"leaderboard"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		return nil, "", err
	}
	var rows []leaderboardRow
	if err := json.Unmarshal(res.Leaderboard, &rows); err != nil {
		return nil, "", err
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s:%d:%d:%d:%v", r.ParticipantID, r.Draft, r.Bonus, r.Total, r.HasDraft == nil || *r.HasDraft))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, ";")))
	return res.Leaderboard, hex.EncodeToString(sum[:]), nil
}

// takeScoreSnapshot saves the current standings as week's, unless that week's scores post (request_key)
// was already sent: then the saved snapshot is frozen and returned as is.
func (s *Server) takeScoreSnapshot(c *gin.Context) {
	instanceID, ok := s.automationInstance(c)
	if !ok {
		return
	}
	week, err := strconv.Atoi(c.Param("week"))
	var req struct {
		RequestKey string `json:"request_key"`
	}
	if err != nil || week <= 0 || c.ShouldBindJSON(&req) != nil || req.RequestKey == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "a positive week and the week's scores post request_key are required"})
		return
	}
	ctx := c.Request.Context()
	var sent bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM announcements a JOIN instances i ON i.id = a.instance_id
		WHERE i.public_id = $1 AND a.request_key = $2 AND a.status IN ('sending', 'sent'))`, toPGUUID(instanceID), req.RequestKey).Scan(&sent)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if !sent {
		board, fingerprint, err := s.scoreStandings(ctx, instanceID)
		if err != nil {
			writeAnnouncementError(c, err)
			return
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO score_snapshots (instance_id, week, fingerprint, leaderboard, taken_at)
			SELECT id, $2, $3, $4, $5 FROM instances WHERE public_id = $1
			ON CONFLICT (instance_id, week) DO UPDATE SET fingerprint = EXCLUDED.fingerprint, leaderboard = EXCLUDED.leaderboard, taken_at = EXCLUDED.taken_at`,
			toPGUUID(instanceID), week, fingerprint, []byte(board), s.now()); err != nil {
			writeAnnouncementError(c, err)
			return
		}
	}
	s.writeScoreSnapshot(c, instanceID, week, sent)
}

func (s *Server) getScoreSnapshot(c *gin.Context) {
	instanceID, ok := s.automationInstance(c)
	if !ok {
		return
	}
	week, err := strconv.Atoi(c.Param("week"))
	if err != nil || week <= 0 {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "a positive week is required"})
		return
	}
	s.writeScoreSnapshot(c, instanceID, week, false)
}

func (s *Server) writeScoreSnapshot(c *gin.Context, instanceID uuid.UUID, week int, frozen bool) {
	var fingerprint string
	var board []byte
	var taken time.Time
	err := s.pool.QueryRow(c.Request.Context(), `SELECT s.fingerprint, s.leaderboard, s.taken_at FROM score_snapshots s
		JOIN instances i ON i.id = s.instance_id WHERE i.public_id = $1 AND s.week = $2`, toPGUUID(instanceID), week).Scan(&fingerprint, &board, &taken)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "no snapshot for that week"})
		return
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"week": week, "fingerprint": fingerprint, "leaderboard": json.RawMessage(board), "taken_at": taken.UTC(), "frozen": frozen})
}

// ── Automated scores posts ──────────────────────────────────────────────────────────────────────────

// upsertScorePost saves automation's scores post for approval, tied to the standings it was written from.
// Repeating with the same standings changes nothing. If standings moved: an untouched draft is replaced
// with the new text; an admin-edited one keeps the admin's text. Either way any approval is withdrawn and
// the admins are asked again. send_at may be up to 3 hours past (it then sends on approval); a sent post is
// never touched.
func (s *Server) upsertScorePost(c *gin.Context) {
	instanceID, ok := s.automationInstance(c)
	if !ok {
		return
	}
	key := c.Param("requestKey")
	var req struct {
		GuildID          string    `json:"guild_id"`
		ChannelID        string    `json:"channel_id"`
		Body             string    `json:"body"`
		SendAt           time.Time `json:"send_at"`
		ScoreFingerprint string    `json:"score_fingerprint"`
		NotifyUsers      bool      `json:"notify_users"`
		AwaitingResults  bool      `json:"awaiting_results"` // the body has a placeholder for a game not scored yet
		// Once the game is scored, an admin-edited draft keeps its words: game_results replaces the
		// results_placeholder line in it (and the post is re-asked).
		GameResults        string `json:"game_results"`
		ResultsPlaceholder string `json:"results_placeholder"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(key) == 0 || len(key) > 120 || !validAnnouncementID(req.GuildID) || !validAnnouncementID(req.ChannelID) ||
		strings.TrimSpace(req.Body) == "" || utf8.RuneCountInString(req.Body) > 2000 || req.SendAt.IsZero() {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "guild_id, channel_id, a nonblank body (at most 2000 characters) and send_at are required"})
		return
	}
	ctx := c.Request.Context()
	now := s.now()
	if req.SendAt.Before(now.Add(-3 * time.Hour)) {
		c.JSON(http.StatusConflict, errorResponse{Error: "send_at is more than 3 hours past; the post has expired"})
		return
	}
	// Without a score_fingerprint it's a plain automated post (a game reminder): no standings to keep fresh.
	if req.ScoreFingerprint == "" {
	} else if _, current, err := s.scoreStandings(ctx, instanceID); err != nil {
		writeAnnouncementError(c, err)
		return
	} else if current != req.ScoreFingerprint {
		c.JSON(http.StatusConflict, errorResponse{Error: "standings changed since this draft was written; retake the snapshot"})
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	var bound bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM discord_channel_bindings b JOIN instances i ON i.id = b.instance_id
		WHERE i.public_id = $1 AND b.guild_id = $2 AND b.channel_id = $3)`, toPGUUID(instanceID), req.GuildID, req.ChannelID).Scan(&bound); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if !bound {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "that channel isn't bound to this season"})
		return
	}
	var status, storedBody string
	var fingerprint, autoMD5 *string
	var edited, sameDraft, awaiting bool
	err = tx.QueryRow(ctx, `SELECT a.status, a.score_fingerprint, a.auto_body_md5, a.auto_body_md5 IS DISTINCT FROM md5(a.body), a.auto_body_md5 = md5($3),
			a.body, a.awaiting_results
		FROM announcements a JOIN instances i ON i.id = a.instance_id WHERE i.public_id = $1 AND a.request_key = $2 FOR UPDATE OF a`,
		toPGUUID(instanceID), key, req.Body).Scan(&status, &fingerprint, &autoMD5, &edited, &sameDraft, &storedBody, &awaiting)
	action := ""
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		action = "created"
		_, err = tx.Exec(ctx, `INSERT INTO announcements (instance_id, guild_id, channel_id, request_key, body, due_at, status, notify_users,
				approval_gated, approval_send_at, score_fingerprint, auto_body_md5, awaiting_results)
			SELECT id, $2, $3, $4, $5, $6, 'draft', $7, true, $6, NULLIF($8, ''), md5($5), $9 FROM instances WHERE public_id = $1`,
			toPGUUID(instanceID), req.GuildID, req.ChannelID, key, req.Body, req.SendAt.UTC(), req.NotifyUsers, req.ScoreFingerprint, req.AwaitingResults)
	case err != nil:
	case status == "sent" || status == "sending":
		action = "sent"
	case edited && awaiting && !req.AwaitingResults && req.ScoreFingerprint != "": // the game is scored: fill in the admin's draft
		action = "results-inserted"
		merged, ok := insertResults(storedBody, req.ResultsPlaceholder, req.GameResults)
		if !ok {
			action = "results-missing" // stays held (awaiting_results) and never sends with the placeholder
			_, err = tx.Exec(ctx, `INSERT INTO admin_alerts (instance_id, alert_key, body) SELECT id, $2, $3 FROM instances WHERE public_id = $1
				ON CONFLICT (instance_id, alert_key) DO NOTHING`, toPGUUID(instanceID), key+"-results-missing",
				message("scores-results-missing.md", map[string]string{"Key": key}))
			break
		}
		// Keeps auto_body_md5, so the post still counts as admin-edited for later standings changes.
		_, err = tx.Exec(ctx, `UPDATE announcements a SET body = $3, score_fingerprint = $4, status = 'draft', scheduled_at = NULL, approval_gated = true,
				approval_send_at = $5, approval_claimed_at = NULL, approval_notified_at = NULL, awaiting_results = false
			FROM instances i WHERE i.id = a.instance_id AND i.public_id = $1 AND a.request_key = $2`,
			toPGUUID(instanceID), key, merged, req.ScoreFingerprint, req.SendAt.UTC())
		if err == nil && (fingerprint == nil || *fingerprint != req.ScoreFingerprint) {
			_, err = tx.Exec(ctx, `INSERT INTO admin_alerts (instance_id, alert_key, body) SELECT id, $2, $3 FROM instances WHERE public_id = $1
				ON CONFLICT (instance_id, alert_key) DO NOTHING`, toPGUUID(instanceID), key+"-rescored-"+req.ScoreFingerprint[:12],
				message("scores-rescored.md", map[string]string{"Key": key}))
		}
	case fingerprint != nil && *fingerprint == req.ScoreFingerprint && (edited || sameDraft):
		action = "unchanged" // same standings and an unedited draft with the same text (or an admin's text, kept)
	case autoMD5 == nil:
		c.JSON(http.StatusConflict, errorResponse{Error: key + " exists but wasn't written by automation; leaving it alone"})
		return
	case req.ScoreFingerprint == "" && (edited || sameDraft): // plain post: keep the admin's words
		action = "unchanged"
	case edited: // keep the admin's words; just re-ask with the new standings noted
		action = "rehold-edited"
		_, err = tx.Exec(ctx, `UPDATE announcements a SET score_fingerprint = $3, status = 'draft', scheduled_at = NULL, approval_gated = true,
				approval_send_at = $4, approval_claimed_at = NULL, approval_notified_at = NULL
			FROM instances i WHERE i.id = a.instance_id AND i.public_id = $1 AND a.request_key = $2`,
			toPGUUID(instanceID), key, req.ScoreFingerprint, req.SendAt.UTC())
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO admin_alerts (instance_id, alert_key, body) SELECT id, $2, $3 FROM instances WHERE public_id = $1
				ON CONFLICT (instance_id, alert_key) DO NOTHING`, toPGUUID(instanceID), key+"-rescored-"+req.ScoreFingerprint[:12],
				message("scores-rescored.md", map[string]string{"Key": key}))
		}
	default:
		action = "regenerated"
		_, err = tx.Exec(ctx, `UPDATE announcements a SET body = $3, auto_body_md5 = md5($3), score_fingerprint = NULLIF($4, ''), notify_users = $5,
				status = 'draft', scheduled_at = NULL, approval_gated = true, approval_send_at = $6, approval_claimed_at = NULL, approval_notified_at = NULL,
				awaiting_results = $7
			FROM instances i WHERE i.id = a.instance_id AND i.public_id = $1 AND a.request_key = $2`,
			toPGUUID(instanceID), key, req.Body, req.ScoreFingerprint, req.NotifyUsers, req.SendAt.UTC(), req.AwaitingResults)
	}
	if err != nil {
		writeAnnouncementError(c, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeAnnouncementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"action": action})
}

// insertResults puts the game's results in place of the placeholder line(s) of an admin-edited draft. It fails
// unless the placeholder is there as a whole line and no trace of it is left, or the text would be too long.
func insertResults(body, placeholder, results string) (string, bool) {
	placeholder = strings.TrimSpace(placeholder)
	if placeholder == "" || strings.TrimSpace(results) == "" {
		return "", false
	}
	lines := strings.Split(body, "\n")
	found := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == placeholder {
			lines[i] = results
			found++
		}
	}
	merged := strings.Join(lines, "\n")
	if found == 0 || strings.Contains(merged, placeholder) || utf8.RuneCountInString(merged) > 2000 {
		return "", false
	}
	return merged, true
}

// scoresStillCurrent reports whether an automated scores post still describes the live standings
// (posts without a fingerprint always are).
func (s *Server) scoresStillCurrent(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, announcementID any) (bool, error) {
	var fingerprint *string
	var instanceID uuid.UUID
	if err := q.QueryRow(ctx, `SELECT a.score_fingerprint, i.public_id FROM announcements a JOIN instances i ON i.id = a.instance_id WHERE a.id = $1`,
		announcementID).Scan(&fingerprint, &instanceID); err != nil {
		return false, err
	}
	if fingerprint == nil {
		return true, nil
	}
	_, current, err := s.scoreStandings(ctx, instanceID)
	return current == *fingerprint, err
}

// replaceWikiImport swaps a wiki-sourced episode for survivoR's results: it removes what the wiki import
// recorded (its boots and challenge awards; ledger rows cascade), applies the request, and reports whether
// any boot or anyone's points changed.
func (s *Server) replaceWikiImport(ctx context.Context, tx pgx.Tx, qtx *db.Queries, instanceID uuid.UUID, req episodeImportRequest, boots []int32, keys []string) (gin.H, error) {
	positions := append([]int32(nil), boots...)
	newKeys := make([]string, 0, len(req.Challenges)+len(req.ChampionChallenges))
	for _, b := range req.Boots {
		positions = append(positions, int32(b.Position)) // #nosec G115 -- validated against the roster size
	}
	for _, ch := range req.Challenges {
		newKeys = append(newKeys, ch.Key)
	}
	for _, ch := range req.ChampionChallenges {
		newKeys = append(newKeys, ch.Key)
	}
	before, err := importSnapshot(ctx, tx, instanceID, positions, keys)
	if err != nil {
		return nil, err
	}
	id := toPGUUID(instanceID)
	if _, err := tx.Exec(ctx, `DELETE FROM activity_occurrences o USING instance_activities a, instances i
		WHERE o.activity_id = a.id AND a.instance_id = i.id AND i.public_id = $1 AND o.source_ref = ANY($2::text[])`, id, keys); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM outcome_positions o USING instances i WHERE o.instance_id = i.id AND i.public_id = $1 AND o.position = ANY($2::int[])`, id, boots); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM episode_imports e USING instances i WHERE e.instance_id = i.id AND i.public_id = $1 AND e.episode_number = $2`, id, req.EpisodeNumber); err != nil {
		return nil, err
	}
	req.Replace = false
	res, err := s.applyEpisodeImport(ctx, tx, qtx, instanceID, req)
	if err != nil {
		return nil, err
	}
	after, err := importSnapshot(ctx, tx, instanceID, positions, newKeys)
	if err != nil {
		return nil, err
	}
	res["status"] = "replaced"
	res["changed"] = before != after
	return res, nil
}

// importSnapshot describes who sits at positions and each participant's points from the keyed occurrences.
func importSnapshot(ctx context.Context, tx pgx.Tx, instanceID uuid.UUID, positions []int32, keys []string) (string, error) {
	var out string
	err := tx.QueryRow(ctx, `SELECT
		coalesce((SELECT string_agg(o.position || '=' || coalesce(o.contestant_id::text, ''), ';' ORDER BY o.position)
			FROM outcome_positions o JOIN instances i ON i.id = o.instance_id WHERE i.public_id = $1 AND o.position = ANY($2::int[])), '')
		|| '|' ||
		coalesce((SELECT string_agg(p || '=' || n, ';' ORDER BY p) FROM (SELECT l.participant_id::text p, sum(l.points) n
			FROM bonus_point_ledger_entries l JOIN activity_occurrences o ON o.id = l.activity_occurrence_id
			JOIN instance_activities a ON a.id = o.activity_id JOIN instances i ON i.id = a.instance_id
			WHERE i.public_id = $1 AND o.source_ref = ANY($3::text[]) GROUP BY l.participant_id HAVING sum(l.points) <> 0) t), '')`,
		toPGUUID(instanceID), positions, keys).Scan(&out)
	return out, err
}
