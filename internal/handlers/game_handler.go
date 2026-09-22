package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"myapp/internal/services"
	"myapp/internal/utils"
)

// GameHandler serves the skill-game collection: catalog, seeded sessions,
// score submission and leaderboards.
//
// Guests and logged-in customers use the same URLs, exactly like reel social:
// a customer JWT or an X-Guest-Token identifies the player.
type GameHandler struct {
	games *services.GameService
}

func NewGameHandler(gameService *services.GameService) *GameHandler {
	return &GameHandler{games: gameService}
}

// ListGames handles GET /games — the game collection screen. Public.
func (h *GameHandler) ListGames(w http.ResponseWriter, r *http.Request) {
	list, err := h.games.ListGames(r.Context())
	if err != nil {
		h.writeError(w, err, "could not list games")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// GetGame handles GET /games/{slug} — game details and rules. Public.
func (h *GameHandler) GetGame(w http.ResponseWriter, r *http.Request) {
	game, err := h.games.GetGame(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		h.writeError(w, err, "could not get game")
		return
	}
	utils.JSON(w, http.StatusOK, game)
}

// StartSession handles POST /games/{slug}/sessions.
// Returns the server seed the client must use to generate its tiles.
//
// An optional ?level= scales up the difficulty for games that support level
// progression (currently Memory Match) — the harder config it produces is
// baked into the session at creation time, so replay never has to know a
// level was involved.
func (h *GameHandler) StartSession(w http.ResponseWriter, r *http.Request) {
	level := 1
	if raw := r.URL.Query().Get("level"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			level = parsed
		}
	}
	session, err := h.games.StartSession(r.Context(), chi.URLParam(r, "slug"), actorFromContext(r), level)
	if err != nil {
		h.writeError(w, err, "could not start game session")
		return
	}
	utils.JSON(w, http.StatusCreated, session)
}

// submitScoreRequest is the body of a score submission.
//
// There is deliberately no trusted score field. The player sends the moves they
// made; the server replays them and decides what they were worth. client_score
// is optional and used only to detect a tampered or out-of-date app.
type submitScoreRequest struct {
	Moves       []string `json:"moves"`
	ClientScore *int64   `json:"client_score,omitempty"`
}

// SubmitScore handles POST /games/sessions/{sessionID}/submit.
func (h *GameHandler) SubmitScore(w http.ResponseWriter, r *http.Request) {
	var req submitScoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	result, err := h.games.SubmitScore(r.Context(), chi.URLParam(r, "sessionID"), actorFromContext(r),
		services.SubmitScoreInput{Moves: req.Moves, ClientScore: req.ClientScore})
	if err != nil {
		h.writeError(w, err, "could not submit score")
		return
	}
	utils.JSON(w, http.StatusOK, result)
}

// GetLeaderboard handles GET /games/{slug}/leaderboard.
// Public; an optional identity adds the caller's own best score.
func (h *GameHandler) GetLeaderboard(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			utils.Error(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = n
	}

	board, err := h.games.Leaderboard(r.Context(), chi.URLParam(r, "slug"), actorFromContext(r), limit)
	if err != nil {
		h.writeError(w, err, "could not get leaderboard")
		return
	}
	utils.JSON(w, http.StatusOK, board)
}

// writeError maps service errors onto HTTP status codes.
func (h *GameHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrGameNotFound):
		utils.Error(w, http.StatusNotFound, "game not found")
	case errors.Is(err, services.ErrGameSessionNotFound):
		utils.Error(w, http.StatusNotFound, "game session not found")
	case errors.Is(err, services.ErrGameSessionExpired):
		utils.Error(w, http.StatusGone, "game session expired, start a new game")
	case errors.Is(err, services.ErrGameSessionForbidden):
		// Deliberately vague: do not confirm that someone else's session exists.
		utils.Error(w, http.StatusForbidden, "game session not found")
	case errors.Is(err, services.ErrInvalidMoveLog):
		utils.Error(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, services.ErrSocialIdentityRequired):
		utils.Error(w, http.StatusUnauthorized, "sign in or send an X-Guest-Token to play")
	// Official competition attempts are submitted here too.
	case errors.Is(err, services.ErrCompetitionClosed):
		// A score received at or after the close does not count (§13.6).
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrCompetitionNotFound):
		utils.Error(w, http.StatusNotFound, "competition not found")
	default:
		log.Printf("game handler: %s: %v", fallback, err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
