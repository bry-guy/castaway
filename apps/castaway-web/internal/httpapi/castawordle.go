package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bry-guy/srvivor/apps/castaway-web/internal/castawordle"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/db"
	"github.com/bry-guy/srvivor/apps/castaway-web/internal/gameplay"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type castawordleGameView struct {
	ID            string    `json:"id"`
	InstanceID    string    `json:"instance_id"`
	Name          string    `json:"name"`
	WordLength    int       `json:"word_length"`
	GuessLimit    int       `json:"guess_limit"`
	OpensAt       time.Time `json:"opens_at"`
	CutoffAt      time.Time `json:"cutoff_at"`
	Unscored      bool      `json:"unscored"`
	Test          bool      `json:"test"`
	Private       bool      `json:"private,omitempty"` // one player's own scored puzzle (see castawordlePrivateGames)
	EpisodeNumber *int32    `json:"episode_number,omitempty"`
}

// castawordlePrivateGame describes a private puzzle: its owner and the scored game it stands in for.
type castawordlePrivateGame struct {
	OwnerDiscordID string
	ReplacesID     string
	Episode        *int32
}

// castawordlePrivateGames maps game ID to private-puzzle details for an instance (or one game when gameID
// is set). Only the owner may see or play a private puzzle; its result counts in the original's round.
func (s *Server) castawordlePrivateGames(ctx context.Context, instanceID pgtype.UUID, gameID pgtype.UUID) (map[string]castawordlePrivateGame, error) {
	rows, err := s.pool.Query(ctx, `SELECT g.public_id::text, COALESCE(p.discord_user_id, ''), o.public_id::text, o.episode_number
		FROM castawordle_games g JOIN instances i ON i.id = g.instance_id
		JOIN participants p ON p.id = g.player_id JOIN castawordle_games o ON o.id = g.replaces_game_id
		WHERE (i.public_id = $1 OR $1 IS NULL) AND (g.public_id = $2 OR $2 IS NULL)`, instanceID, gameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	games := map[string]castawordlePrivateGame{}
	for rows.Next() {
		var id string
		var game castawordlePrivateGame
		if err := rows.Scan(&id, &game.OwnerDiscordID, &game.ReplacesID, &game.Episode); err != nil {
			return nil, err
		}
		games[id] = game
	}
	return games, rows.Err()
}

// castawordleVisible applies the visibility rules: private puzzles are for their owner only, tests for
// admins only, and a player with a private puzzle doesn't see the game it replaces.
func castawordleVisible(id string, test, admin bool, discordID string, private map[string]castawordlePrivateGame) bool {
	if game, ok := private[id]; ok {
		return game.OwnerDiscordID != "" && game.OwnerDiscordID == discordID
	}
	for _, game := range private {
		if game.ReplacesID == id && game.OwnerDiscordID == discordID {
			return false
		}
	}
	return !test || admin
}

func (view *castawordleGameView) markPrivate(game castawordlePrivateGame) {
	view.Private, view.Test, view.Unscored, view.EpisodeNumber = true, false, false, game.Episode
}

type castawordleGuessView struct {
	Word     string   `json:"word"`
	Feedback []string `json:"feedback"`
}

type castawordlePlayView struct {
	Game    castawordleGameView    `json:"game"`
	Guesses []castawordleGuessView `json:"guesses"`
	Status  string                 `json:"status"`
	Answer  string                 `json:"answer,omitempty"`
}

func castawordleGame(row db.GetCastawordleGameRow) castawordleGameView {
	var episodeNumber *int32
	if row.EpisodeNumber.Valid {
		episodeNumber = &row.EpisodeNumber.Int32
	}
	return castawordleGameView{
		ID: pgUUIDString(row.ID), InstanceID: pgUUIDString(row.InstanceID), Name: row.Name,
		WordLength: len(row.Answer), GuessLimit: castawordle.GuessLimit,
		OpensAt: easternTime(row.OpensAt.Time), CutoffAt: easternTime(row.CutoffAt.Time), Unscored: !row.WordleRoundID.Valid,
		Test: !row.EpisodeNumber.Valid, EpisodeNumber: episodeNumber,
	}
}

func (s *Server) castawordlePlay(row db.GetCastawordleGameRow, guesses []string, status string) castawordlePlayView {
	view := castawordlePlayView{Game: castawordleGame(row), Guesses: make([]castawordleGuessView, 0, len(guesses)), Status: status}
	for _, guess := range guesses {
		view.Guesses = append(view.Guesses, castawordleGuessView{Word: guess, Feedback: castawordle.Feedback(row.Answer, guess)})
	}
	switch {
	case status == "solved" || status == "exhausted":
		view.Answer = row.Answer
	case !s.now().Before(row.CutoffAt.Time):
		view.Status = "expired"
	case s.now().Before(row.OpensAt.Time):
		view.Status = "not_open"
	}
	return view
}

func castawordleError(c *gin.Context, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "game not found"})
		return
	}
	requestLogger.Error("castawordle request", "error", err)
	c.JSON(http.StatusInternalServerError, errorResponse{Error: "could not load or save the game"})
}

func (s *Server) createCastawordle(c *gin.Context) {
	instanceID, ok := parseUUIDPath(c, "instanceID")
	if !ok || !s.requireInstanceAdminRequest(c, instanceID) {
		return
	}
	var req struct {
		Name          string    `json:"name"`
		Answer        string    `json:"answer"`
		OpensAt       time.Time `json:"opens_at"`
		CutoffAt      time.Time `json:"cutoff_at"`
		EpisodeNumber *int32    `json:"episode_number"`
		Scored        bool      `json:"scored"`
		Window        *struct {
			OpensAt  time.Time `json:"opens_at"`
			CutoffAt time.Time `json:"cutoff_at"`
		} `json:"window"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid game configuration"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Answer = castawordle.Normalize(req.Answer)
	if req.Name == "" || len(req.Name) > 80 || !castawordle.ValidWord(req.Answer) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "use a name up to 80 characters and a dictionary answer of 4–8 letters"})
		return
	}
	if req.Scored && req.EpisodeNumber == nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "scored games require an episode number"})
		return
	}
	if req.EpisodeNumber != nil && (!req.OpensAt.IsZero() || !req.CutoffAt.IsZero()) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "scheduled puzzle times come from the instance schedule"})
		return
	}
	if req.EpisodeNumber == nil && req.Window != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "custom windows require an episode number"})
		return
	}

	ctx := c.Request.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		castawordleError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	qtx := s.queries.WithTx(tx)
	instance, err := qtx.LockInstanceForProgression(ctx, toPGUUID(instanceID))
	if err != nil {
		writeWordleError(c, err)
		return
	}
	if err := requireWordleInstanceAdmin(ctx, qtx, instance.ID, c.Request); err != nil {
		writeWordleError(c, err)
		return
	}
	if req.Scored && instance.ProgressionMode != "legacy" {
		c.JSON(http.StatusConflict, errorResponse{Error: "scored Castawordle requires a legacy instance"})
		return
	}

	now := s.now().UTC().Truncate(time.Microsecond)
	var episodeNumber pgtype.Int4
	if req.EpisodeNumber != nil {
		episodeNumber = pgtype.Int4{Int32: *req.EpisodeNumber, Valid: true}
		episodes, err := qtx.ListInstanceEpisodes(ctx, toPGUUID(instanceID))
		if err != nil {
			castawordleError(c, err)
			return
		}
		defaultOpens, defaultCutoff, err := castawordleEpisodeWindow(episodes, *req.EpisodeNumber)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		req.OpensAt, req.CutoffAt = defaultOpens, defaultCutoff
		if req.Window != nil {
			if req.Window.OpensAt.IsZero() || req.Window.CutoffAt.IsZero() {
				c.JSON(http.StatusBadRequest, errorResponse{Error: "window requires opens_at and cutoff_at"})
				return
			}
			req.OpensAt = req.Window.OpensAt
			req.CutoffAt = req.Window.CutoffAt
		}
		defaultOpens = defaultOpens.UTC().Truncate(time.Microsecond)
		defaultCutoff = defaultCutoff.UTC().Truncate(time.Microsecond)
		req.OpensAt = req.OpensAt.UTC().Truncate(time.Microsecond)
		req.CutoffAt = req.CutoffAt.UTC().Truncate(time.Microsecond)
		if req.OpensAt.Before(defaultOpens) || req.OpensAt.After(defaultCutoff) || req.CutoffAt.Before(defaultOpens) || req.CutoffAt.After(defaultCutoff) {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "custom window must be within the episode window"})
			return
		}
	} else {
		if req.OpensAt.IsZero() {
			req.OpensAt = now
		}
		if req.CutoffAt.IsZero() {
			req.CutoffAt = req.OpensAt.AddDate(0, 0, 7)
		}
		req.OpensAt = req.OpensAt.UTC().Truncate(time.Microsecond)
		req.CutoffAt = req.CutoffAt.UTC().Truncate(time.Microsecond)
	}
	if !req.CutoffAt.After(req.OpensAt) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "cutoff must be after opening"})
		return
	}
	if req.EpisodeNumber != nil {
		existing, err := qtx.GetCastawordleGameByEpisode(ctx, db.GetCastawordleGameByEpisodeParams{
			InstanceID: toPGUUID(instanceID), EpisodeNumber: episodeNumber,
		})
		if err == nil {
			if req.Scored && existing.WordleRoundID.Valid && existing.Name == req.Name && existing.Answer == req.Answer && existing.OpensAt.Time.Equal(req.OpensAt) && existing.CutoffAt.Time.Equal(req.CutoffAt) {
				if err := tx.Commit(ctx); err != nil {
					castawordleError(c, err)
					return
				}
				c.JSON(http.StatusOK, castawordleGame(db.GetCastawordleGameRow(existing)))
				return
			}
			c.JSON(http.StatusConflict, errorResponse{Error: "a puzzle is already prepared for this episode"})
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			castawordleError(c, err)
			return
		}
		if !now.Before(req.CutoffAt) {
			c.JSON(http.StatusBadRequest, errorResponse{Error: "this episode's game window has already closed"})
			return
		}
	}

	var roundID pgtype.UUID
	if req.Scored {
		activity, err := qtx.CreateInstanceActivity(ctx, db.CreateInstanceActivityParams{
			InstanceID: toPGUUID(instanceID), ActivityType: wordleActivityType, Name: req.Name, Status: "active",
			StartsAt: wordleTimestamp(req.OpensAt), EndsAt: wordleTimestamp(req.CutoffAt),
			Metadata: []byte(`{"scoring":"individual_and_tribe_average","castawordle":true}`),
		})
		if err != nil {
			writeWordleError(c, err)
			return
		}
		occurrence, err := qtx.CreateActivityOccurrence(ctx, db.CreateActivityOccurrenceParams{
			ActivityID: activity.ID, OccurrenceType: wordleActivityType, Name: req.Name,
			EffectiveAt: wordleTimestamp(req.CutoffAt), Status: "recorded", Metadata: []byte(`{}`),
		})
		if err != nil {
			writeWordleError(c, err)
			return
		}
		roundID, err = qtx.CreateWordleRound(ctx, db.CreateWordleRoundParams{
			ActivityID: activity.ID, ActivityOccurrenceID: occurrence.ID,
			RoundKey: fmt.Sprintf("episode-%d", *req.EpisodeNumber),
			OpensAt:  wordleTimestamp(req.OpensAt), CutoffAt: wordleTimestamp(req.CutoffAt),
		})
		if err != nil {
			writeWordleError(c, err)
			return
		}
	}
	row, err := qtx.CreateCastawordleGame(ctx, db.CreateCastawordleGameParams{
		InstanceID: toPGUUID(instanceID), Name: req.Name, Answer: req.Answer,
		DictionaryVersion: castawordle.DictionaryVersion, EpisodeNumber: episodeNumber,
		OpensAt: wordleTimestamp(req.OpensAt), CutoffAt: wordleTimestamp(req.CutoffAt),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			c.JSON(http.StatusConflict, errorResponse{Error: "a puzzle is already prepared for this episode"})
			return
		}
		castawordleError(c, err)
		return
	}
	if req.Scored {
		if err := qtx.UpdateCastawordleGameWordleRound(ctx, db.UpdateCastawordleGameWordleRoundParams{ID: row.ID, WordleRoundID: roundID}); err != nil {
			writeWordleError(c, err)
			return
		}
		row.WordleRoundID = roundID
	}
	if err := tx.Commit(ctx); err != nil {
		castawordleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, castawordleGame(db.GetCastawordleGameRow(row)))
}

func (s *Server) castawordlePlayer(c *gin.Context) (db.GetCastawordleGameRow, pgtype.UUID, bool) {
	id, ok := parseUUIDPath(c, "gameID")
	if !ok {
		return db.GetCastawordleGameRow{}, pgtype.UUID{}, false
	}
	game, err := s.queries.GetCastawordleGame(c.Request.Context(), toPGUUID(id))
	if err != nil {
		castawordleError(c, err)
		return game, pgtype.UUID{}, false
	}
	if strings.HasPrefix(c.FullPath(), "/api/") && s.public.InstanceID != pgUUIDString(game.InstanceID) {
		c.JSON(http.StatusForbidden, errorResponse{Error: "this game belongs to a different season"})
		return game, pgtype.UUID{}, false
	}
	private, err := s.castawordlePrivateGames(c.Request.Context(), game.InstanceID, pgtype.UUID{})
	if err != nil {
		castawordleError(c, err)
		return game, pgtype.UUID{}, false
	}
	admin := false
	if _, isPrivate := private[pgUUIDString(game.ID)]; !game.EpisodeNumber.Valid && !isPrivate {
		if admin, err = s.isInstanceAdmin(c.Request.Context(), game.InstanceID, discordUserIDFromRequest(c.Request)); err != nil {
			castawordleError(c, err)
			return game, pgtype.UUID{}, false
		}
	}
	if !castawordleVisible(pgUUIDString(game.ID), !game.EpisodeNumber.Valid, admin, discordUserIDFromRequest(c.Request), private) {
		c.JSON(http.StatusNotFound, errorResponse{Error: "game not found"})
		return game, pgtype.UUID{}, false
	}
	player, err := s.queries.GetParticipantByDiscordUserID(c.Request.Context(), db.GetParticipantByDiscordUserIDParams{
		InstanceID:    game.InstanceID,
		DiscordUserID: pgtype.Text{String: discordUserIDFromRequest(c.Request), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusForbidden, errorResponse{Error: "link your Discord account to a player in this season to play"})
		return game, pgtype.UUID{}, false
	}
	if err != nil {
		castawordleError(c, err)
		return game, pgtype.UUID{}, false
	}
	return game, player.ID, true
}

func (s *Server) getCastawordlePlay(c *gin.Context) {
	game, participantID, ok := s.castawordlePlayer(c)
	if !ok {
		return
	}
	play, err := s.queries.GetCastawordlePlay(c.Request.Context(), db.GetCastawordlePlayParams{GameID: game.ID, ParticipantID: participantID})
	if errors.Is(err, pgx.ErrNoRows) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, s.castawordlePlay(game, nil, "not_started"))
		return
	}
	if err != nil {
		castawordleError(c, err)
		return
	}
	var guesses []string
	if err := json.Unmarshal(play.Guesses, &guesses); err != nil {
		castawordleError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.castawordlePlay(game, guesses, play.Status))
}

func (s *Server) guessCastawordle(c *gin.Context) {
	game, participantID, ok := s.castawordlePlayer(c)
	if !ok {
		return
	}
	var req struct {
		Guess    string `json:"guess"`
		Position int    `json:"position"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	if err := c.ShouldBindJSON(&req); err != nil || req.Position < 1 || req.Position > castawordle.GuessLimit {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid guess request"})
		return
	}
	req.Guess = castawordle.Normalize(req.Guess)
	tx, err := s.pool.Begin(c.Request.Context())
	if err != nil {
		castawordleError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	qtx := s.queries.WithTx(tx)
	lockedGame, err := qtx.LockCastawordleGame(c.Request.Context(), game.ID)
	if err != nil {
		castawordleError(c, err)
		return
	}
	game = db.GetCastawordleGameRow(lockedGame)
	if err := qtx.EnsureCastawordlePlay(c.Request.Context(), db.EnsureCastawordlePlayParams{GameID: game.ID, ParticipantID: participantID}); err != nil {
		castawordleError(c, err)
		return
	}
	play, err := qtx.LockCastawordlePlay(c.Request.Context(), db.LockCastawordlePlayParams{GameID: game.ID, ParticipantID: participantID})
	if err != nil {
		castawordleError(c, err)
		return
	}
	var guesses []string
	if err := json.Unmarshal(play.Guesses, &guesses); err != nil {
		castawordleError(c, err)
		return
	}
	if req.Position <= len(guesses) && guesses[req.Position-1] == req.Guess {
		c.JSON(http.StatusOK, s.castawordlePlay(game, guesses, play.Status))
		return
	}
	if len(req.Guess) != len(game.Answer) || !castawordle.ValidWord(req.Guess) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid word, try again"})
		return
	}
	if req.Position != len(guesses)+1 || play.Status != "in_progress" {
		c.JSON(http.StatusConflict, errorResponse{Error: "your game changed; reload your saved progress"})
		return
	}
	now := s.now()
	if now.Before(game.OpensAt.Time) || !now.Before(game.CutoffAt.Time) {
		c.JSON(http.StatusConflict, errorResponse{Error: "this game is not open"})
		return
	}
	guesses = append(guesses, req.Guess)
	status := "in_progress"
	if req.Guess == game.Answer {
		status = "solved"
	} else if len(guesses) == castawordle.GuessLimit {
		status = "exhausted"
	}
	encoded, err := json.Marshal(guesses)
	if err != nil {
		castawordleError(c, err)
		return
	}
	if err := qtx.UpdateCastawordlePlay(c.Request.Context(), db.UpdateCastawordlePlayParams{
		GameID: game.ID, ParticipantID: participantID, Guesses: encoded, Status: status,
		UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}); err != nil {
		castawordleError(c, err)
		return
	}
	if err := tx.Commit(c.Request.Context()); err != nil {
		castawordleError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.castawordlePlay(game, guesses, status))
}

func (s *Server) resolveCastawordle(c *gin.Context) {
	gameID, ok := parseUUIDPath(c, "gameID")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	game, err := s.queries.GetCastawordleGame(ctx, toPGUUID(gameID))
	if err != nil {
		castawordleError(c, err)
		return
	}
	if strings.HasPrefix(c.FullPath(), "/api/") && s.public.InstanceID != pgUUIDString(game.InstanceID) {
		c.JSON(http.StatusForbidden, errorResponse{Error: "this game belongs to a different season"})
		return
	}
	if !s.requireInstanceAdminRequest(c, uuid.UUID(game.InstanceID.Bytes)) {
		return
	}
	if !game.WordleRoundID.Valid {
		c.JSON(http.StatusConflict, errorResponse{Error: "unscored games cannot be resolved"})
		return
	}

	payload, err := s.resolveCastawordleGame(ctx, game, c.Request)
	if err != nil {
		writeWordleError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

// resolveCastawordleGame scores a scored game's round once past cutoff; authReq nil means the system resolver.
func (s *Server) resolveCastawordleGame(ctx context.Context, game db.GetCastawordleGameRow, authReq *http.Request) ([]byte, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			requestLogger.Error("rollback castawordle resolve", "error", err)
		}
	}()
	qtx := s.queries.WithTx(tx)
	instance, err := qtx.LockInstanceForProgression(ctx, game.InstanceID)
	if err != nil {
		return nil, err
	}
	if instance.ProgressionMode != "legacy" {
		return nil, &progressionFailure{status: http.StatusConflict, message: "Wordle lifecycle is only supported for legacy instances"}
	}
	if authReq != nil {
		if err := requireWordleInstanceAdmin(ctx, qtx, instance.ID, authReq); err != nil {
			return nil, err
		}
	}
	round, err := qtx.LockWordleRound(ctx, game.WordleRoundID)
	if err != nil {
		return nil, err
	}
	if !sameWordleUUID(round.InstanceID, game.InstanceID) || round.ActivityType != wordleActivityType {
		return nil, &progressionFailure{status: http.StatusConflict, message: "game is not linked to a tribe Wordle round"}
	}
	lockedGame, err := qtx.LockCastawordleGame(ctx, game.ID)
	if err != nil {
		return nil, err
	}
	if !lockedGame.WordleRoundID.Valid || !sameWordleUUID(lockedGame.WordleRoundID, game.WordleRoundID) {
		return nil, &progressionFailure{status: http.StatusConflict, message: "game scoring link changed"}
	}
	if len(round.ResolutionResponse) > 0 {
		payload := append([]byte(nil), round.ResolutionResponse...)
		return payload, tx.Commit(ctx)
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	if now.Before(round.CutoffAt.Time) {
		return nil, &progressionFailure{status: http.StatusConflict, message: "Castawordle cannot resolve before cutoff"}
	}
	if round.OccurrenceStatus != "recorded" {
		return nil, &progressionFailure{status: http.StatusConflict, message: "Castawordle round is not resolvable"}
	}
	if !round.OpensAt.Time.Equal(lockedGame.OpensAt.Time) || !round.CutoffAt.Time.Equal(lockedGame.CutoffAt.Time) {
		return nil, &progressionFailure{status: http.StatusConflict, message: "Castawordle game and round windows do not match"}
	}

	plays, err := qtx.ListCastawordlePlays(ctx, lockedGame.ID)
	if err != nil {
		return nil, err
	}
	plays, answers, err := withPrivateCastawordlePlays(ctx, tx, lockedGame.ID, lockedGame.Answer, plays)
	if err != nil {
		return nil, err
	}
	type completedPlay struct {
		participantID pgtype.UUID
		guessCount    int32
	}
	completed := make([]completedPlay, 0, len(plays))
	for _, play := range plays {
		var guesses []string
		if err := json.Unmarshal(play.Guesses, &guesses); err != nil {
			return nil, err
		}
		switch play.Status {
		case "solved":
			if len(guesses) < 1 || len(guesses) > castawordle.GuessLimit || guesses[len(guesses)-1] != answers[play.ParticipantID] {
				return nil, &progressionFailure{status: http.StatusConflict, message: "saved Castawordle result is invalid"}
			}
			completed = append(completed, completedPlay{participantID: play.ParticipantID, guessCount: int32(len(guesses))}) // #nosec G115 -- bounded by GuessLimit above
		case "exhausted":
			if len(guesses) != castawordle.GuessLimit {
				return nil, &progressionFailure{status: http.StatusConflict, message: "saved Castawordle result is invalid"}
			}
			completed = append(completed, completedPlay{participantID: play.ParticipantID, guessCount: 7})
		case "in_progress":
		default:
			return nil, &progressionFailure{status: http.StatusConflict, message: "saved Castawordle result is invalid"}
		}
	}

	if len(completed) > 0 {
		memberships, err := qtx.ListInstanceTribeMembershipsAt(ctx, db.ListInstanceTribeMembershipsAtParams{
			InstanceID: game.InstanceID, At: wordleTimestamp(round.CutoffAt.Time),
		})
		if err != nil {
			return nil, err
		}
		byParticipant := make(map[pgtype.UUID][]db.ListInstanceTribeMembershipsAtRow, len(memberships))
		for _, membership := range memberships {
			byParticipant[membership.ParticipantID] = append(byParticipant[membership.ParticipantID], membership)
		}
		for _, result := range completed {
			eligible := byParticipant[result.participantID]
			if len(eligible) != 1 {
				return nil, &progressionFailure{status: http.StatusConflict, message: "completed players must have exactly one tribe at cutoff"}
			}
			metadata, err := json.Marshal(map[string]int32{"guess_count": result.guessCount})
			if err != nil {
				return nil, err
			}
			if _, err := qtx.UpsertActivityOccurrenceParticipant(ctx, db.UpsertActivityOccurrenceParticipantParams{
				ActivityOccurrenceID: game.WordleRoundID,
				ParticipantID:        result.participantID, ParticipantGroupID: eligible[0].ParticipantGroupID,
				Role: wordleParticipantRole, Metadata: metadata,
			}); err != nil {
				return nil, err
			}
		}
	}

	if !round.ClosedAt.Valid {
		closed, err := qtx.UpdateWordleRoundClosedAt(ctx, db.UpdateWordleRoundClosedAtParams{ID: game.WordleRoundID, ClosedAt: wordleTimestamp(now)})
		if err != nil {
			return nil, err
		}
		round.ClosedAt = closed.ClosedAt
	}
	var createdEntries []db.CreateBonusPointLedgerEntryRow
	if len(completed) == 0 {
		occurrence, err := qtx.GetActivityOccurrence(ctx, game.WordleRoundID)
		if err != nil {
			return nil, err
		}
		if _, err := qtx.UpdateActivityOccurrenceStatusAndMetadata(ctx, db.UpdateActivityOccurrenceStatusAndMetadataParams{
			ID: game.WordleRoundID, Status: "resolved", EndsAt: wordleTimestamp(now), Metadata: occurrence.Metadata,
		}); err != nil {
			return nil, err
		}
	} else {
		createdEntries, err = gameplay.NewService(qtx).ResolveActivityOccurrence(ctx, game.WordleRoundID)
		if err != nil {
			return nil, err
		}
	}
	entries := make([]gin.H, 0, len(createdEntries))
	for _, entry := range createdEntries {
		entries = append(entries, gin.H{
			"id": pgUUIDString(entry.ID), "instance_id": pgUUIDString(entry.InstanceID),
			"participant_id": pgUUIDString(entry.ParticipantID), "activity_occurrence_id": pgUUIDString(entry.ActivityOccurrenceID),
			"source_group_id": pgUUIDPointer(entry.SourceGroupID), "entry_kind": entry.EntryKind, "points": entry.Points,
			"visibility": entry.Visibility, "reason": entry.Reason, "effective_at": formatTimestamp(entry.EffectiveAt),
			"award_key": pgTextPointer(entry.AwardKey), "metadata": json.RawMessage(entry.Metadata), "created_at": formatTimestamp(entry.CreatedAt),
		})
	}
	payload, err := json.Marshal(gin.H{"created_entries": entries, "created_count": len(entries)})
	if err != nil {
		return nil, err
	}
	stored, err := qtx.UpdateWordleRoundResolution(ctx, db.UpdateWordleRoundResolutionParams{ID: game.WordleRoundID, ResolutionResponse: payload})
	if err != nil {
		return nil, err
	}
	payload = append([]byte(nil), stored.ResolutionResponse...)
	return payload, tx.Commit(ctx)
}

// gamesPage lists a season's games by episode: the current season, or a past league season with ?season=.
func (s *Server) gamesPage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed {
		renderSite(c, "home.html", data)
		return
	}
	id, err := uuid.Parse(s.public.InstanceID)
	if season := c.Query("season"); season != "" && season != s.public.InstanceID {
		id, err = uuid.Parse(season)
		if err == nil {
			err = s.pool.QueryRow(c.Request.Context(), `SELECT name FROM instances WHERE public_id::text = ANY($1) AND public_id = $2`, s.public.LeagueInstanceIDs, id).Scan(&data.SeasonName)
		}
		if err != nil {
			c.String(http.StatusNotFound, "season not found")
			return
		}
		data.SeasonID, data.Admin = id.String(), false // past seasons: read-only, no tests
	}
	if err != nil {
		castawordleError(c, err)
		return
	}
	rows, err := s.queries.ListCastawordleGames(c.Request.Context(), db.ListCastawordleGamesParams{InstanceID: toPGUUID(id), IncludeTests: data.Admin})
	if err != nil {
		castawordleError(c, err)
		return
	}
	private, err := s.castawordlePrivateGames(c.Request.Context(), toPGUUID(id), pgtype.UUID{})
	if err != nil {
		castawordleError(c, err)
		return
	}
	for _, row := range rows {
		gameID := pgUUIDString(row.ID)
		if !castawordleVisible(gameID, !row.EpisodeNumber.Valid, data.Admin, data.User.DiscordUserID, private) {
			continue
		}
		var episodeNumber *int32
		if row.EpisodeNumber.Valid {
			episodeNumber = &row.EpisodeNumber.Int32
		}
		data.Games = append(data.Games, castawordleGameView{
			ID: pgUUIDString(row.ID), InstanceID: pgUUIDString(row.InstanceID), Name: row.Name,
			WordLength: int(row.WordLength), GuessLimit: castawordle.GuessLimit,
			OpensAt: easternTime(row.OpensAt.Time), CutoffAt: easternTime(row.CutoffAt.Time), Unscored: !row.WordleRoundID.Valid,
			Test: !row.EpisodeNumber.Valid, EpisodeNumber: episodeNumber,
		})
		if game, ok := private[gameID]; ok {
			data.Games[len(data.Games)-1].markPrivate(game)
		}
	}
	if data.Admin {
		episodes, err := s.queries.ListInstanceEpisodes(c.Request.Context(), toPGUUID(id))
		if err != nil {
			castawordleError(c, err)
			return
		}
		prepared := make(map[int32]bool)
		for _, row := range rows {
			if row.EpisodeNumber.Valid {
				prepared[row.EpisodeNumber.Int32] = true
			}
		}
		for _, episode := range episodes {
			opens, cutoff, err := castawordleEpisodeWindow(episodes, episode.EpisodeNumber)
			if err == nil && !prepared[episode.EpisodeNumber] && s.now().Before(cutoff) {
				data.Episodes = append(data.Episodes, castawordleEpisodeOption{Number: episode.EpisodeNumber, OpensAt: opens})
			}
		}
	}
	s.castawordleEntries(&data)
	if err := s.addScrambleGames(c.Request.Context(), &data, id.String()); err != nil {
		castawordleError(c, err)
		return
	}
	if err := s.addTrackGames(c.Request.Context(), &data, id.String()); err != nil {
		castawordleError(c, err)
		return
	}
	if err := s.addFreeTheBlockGames(c.Request.Context(), &data, id.String()); err != nil {
		castawordleError(c, err)
		return
	}
	if err := s.addChampionGame(c.Request.Context(), &data, id); err != nil {
		castawordleError(c, err)
		return
	}
	if err := s.addButtonGames(c.Request.Context(), &data, id.String()); err != nil {
		castawordleError(c, err)
		return
	}
	renderSite(c, "games.html", data)
}

func (s *Server) castawordlePage(c *gin.Context) {
	data, ok := s.siteData(c)
	if !ok {
		return
	}
	if !data.Allowed {
		renderSite(c, "home.html", data)
		return
	}
	id, ok := parseUUIDPath(c, "gameID")
	if !ok {
		return
	}
	row, err := s.queries.GetCastawordleGame(c.Request.Context(), toPGUUID(id))
	if err != nil {
		castawordleError(c, err)
		return
	}
	private, err := s.castawordlePrivateGames(c.Request.Context(), row.InstanceID, pgtype.UUID{})
	if err != nil {
		castawordleError(c, err)
		return
	}
	if pgUUIDString(row.InstanceID) != s.public.InstanceID || !castawordleVisible(pgUUIDString(row.ID), !row.EpisodeNumber.Valid, data.Admin, data.User.DiscordUserID, private) {
		c.String(http.StatusNotFound, "game not found")
		return
	}
	game := castawordleGame(row)
	if p, ok := private[pgUUIDString(row.ID)]; ok {
		game.markPrivate(p)
	}
	data.Game = &game
	if data.Closed = !s.now().Before(row.CutoffAt.Time); data.Closed {
		data.Answer = row.Answer
		if data.CastawordleResults, err = s.castawordleResults(c.Request.Context(), row.ID, row.Answer); err != nil {
			castawordleError(c, err)
			return
		}
	}
	renderSite(c, "game.html", data)
}

// easternTime shows game times in the season's Eastern time zone.
func easternTime(t time.Time) time.Time {
	if location, err := time.LoadLocation("America/New_York"); err == nil {
		return t.In(location)
	}
	return t
}

// withPrivateCastawordlePlays swaps in private-puzzle results: a player with a private puzzle for this game
// is scored on it (against its own answer), never on the original. It returns each player's answer.
func withPrivateCastawordlePlays(ctx context.Context, tx pgx.Tx, gameID pgtype.UUID, answer string, plays []db.ListCastawordlePlaysRow) ([]db.ListCastawordlePlaysRow, map[pgtype.UUID]string, error) {
	rows, err := tx.Query(ctx, `SELECT p.public_id, g.answer, cp.guesses, cp.status
		FROM castawordle_games g JOIN castawordle_games o ON o.id = g.replaces_game_id
		JOIN participants p ON p.id = g.player_id
		LEFT JOIN castawordle_plays cp ON cp.game_id = g.id AND cp.participant_id = g.player_id
		WHERE o.public_id = $1 FOR UPDATE OF g`, gameID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	answers := map[pgtype.UUID]string{}
	var private []db.ListCastawordlePlaysRow
	for rows.Next() {
		var play db.ListCastawordlePlaysRow
		var own string
		var status *string
		if err := rows.Scan(&play.ParticipantID, &own, &play.Guesses, &status); err != nil {
			return nil, nil, err
		}
		answers[play.ParticipantID] = own
		if status != nil {
			play.Status = *status
			private = append(private, play)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	merged := private
	for _, play := range plays {
		if _, replaced := answers[play.ParticipantID]; !replaced {
			answers[play.ParticipantID] = answer
			merged = append(merged, play)
		}
	}
	return merged, answers, nil
}

// createPrivateCastawordle gives one player their own puzzle for a scored game: same window, their result
// replaces their result in the original's round. It refuses once they've started the original or it closed.
func (s *Server) createPrivateCastawordle(c *gin.Context) {
	gameID, ok := parseUUIDPath(c, "gameID")
	if !ok {
		return
	}
	var req struct {
		ParticipantID uuid.UUID `json:"participant_id" binding:"required"`
		Name          string    `json:"name"`
		Answer        string    `json:"answer"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "participant_id, name and answer are required"})
		return
	}
	req.Name, req.Answer = strings.TrimSpace(req.Name), castawordle.Normalize(req.Answer)
	if req.Name == "" || len(req.Name) > 80 || !castawordle.ValidWord(req.Answer) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "use a name up to 80 characters and a dictionary answer of 4–8 letters"})
		return
	}
	ctx := c.Request.Context()
	game, err := s.queries.GetCastawordleGame(ctx, toPGUUID(gameID))
	if err != nil {
		castawordleError(c, err)
		return
	}
	if !s.requireInstanceAdminRequest(c, uuid.UUID(game.InstanceID.Bytes)) {
		return
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		castawordleError(c, err)
		return
	}
	defer rollbackTx(c, tx)
	locked, err := s.queries.WithTx(tx).LockCastawordleGame(ctx, game.ID) // guesses lock this row too
	if err != nil {
		castawordleError(c, err)
		return
	}
	if !locked.WordleRoundID.Valid || !s.now().Before(locked.CutoffAt.Time) {
		c.JSON(http.StatusConflict, errorResponse{Error: "private puzzles replace results in an open, scored game"})
		return
	}
	var started bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM castawordle_plays cp JOIN castawordle_games g ON g.id = cp.game_id
		JOIN participants p ON p.id = cp.participant_id WHERE g.public_id = $1 AND p.public_id = $2 AND jsonb_array_length(cp.guesses) > 0)`,
		game.ID, req.ParticipantID).Scan(&started); err != nil {
		castawordleError(c, err)
		return
	}
	if started {
		c.JSON(http.StatusConflict, errorResponse{Error: "that player already started this puzzle"})
		return
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO castawordle_games (instance_id, name, answer, dictionary_version, opens_at, cutoff_at, replaces_game_id, player_id)
		SELECT o.instance_id, $2, $3, $4, o.opens_at, o.cutoff_at, o.id, p.id
		FROM castawordle_games o JOIN participants p ON p.instance_id = o.instance_id AND p.public_id = $5
		WHERE o.public_id = $1 ON CONFLICT DO NOTHING RETURNING public_id`,
		game.ID, req.Name, req.Answer, castawordle.DictionaryVersion, req.ParticipantID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusConflict, errorResponse{Error: "that player isn't in this season or already has a private puzzle for this game"})
		return
	}
	if err != nil {
		castawordleError(c, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		castawordleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "word_length": len(req.Answer)})
}
