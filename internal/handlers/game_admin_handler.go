package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"myapp/internal/services"
	"myapp/internal/utils"
)

// Superadmin views of the game collection: activity per game, full
// leaderboards with real names, and the anti-cheat review queue.

func queryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// AdminListGames handles GET /admin/games.
func (h *GameHandler) AdminListGames(w http.ResponseWriter, r *http.Request) {
	list, err := h.games.AdminGames(r.Context())
	if err != nil {
		h.writeAdminError(w, err, "could not list games")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": list})
}

// AdminGameLeaderboard handles GET /admin/games/{slug}/leaderboard.
func (h *GameHandler) AdminGameLeaderboard(w http.ResponseWriter, r *http.Request) {
	board, err := h.games.AdminGameLeaderboard(r.Context(), chi.URLParam(r, "slug"), queryLimit(r))
	if err != nil {
		h.writeAdminError(w, err, "could not get leaderboard")
		return
	}
	utils.JSON(w, http.StatusOK, board)
}

// AdminGameScores handles GET /admin/games/{slug}/scores?status=manual_review.
func (h *GameHandler) AdminGameScores(w http.ResponseWriter, r *http.Request) {
	items, err := h.games.AdminGameScores(r.Context(), chi.URLParam(r, "slug"),
		r.URL.Query().Get("status"), queryLimit(r))
	if err != nil {
		h.writeAdminError(w, err, "could not list scores")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// AdminReviewScore handles POST /admin/games/scores/{sessionID}/review.
func (h *GameHandler) AdminReviewScore(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminActor(w, r)
	if !ok {
		return
	}
	sessionID, ok := uuidParam(w, r, "sessionID", "score not found")
	if !ok {
		return
	}
	var in services.ReviewInput
	if !decodeBody(w, r, &in) {
		return
	}
	if err := h.games.ReviewGameScore(r.Context(), admin, sessionID, in); err != nil {
		h.writeAdminError(w, err, "could not review score")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *GameHandler) writeAdminError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidReview):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrScoreNotReviewable):
		utils.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, services.ErrGameSessionNotFound):
		utils.Error(w, http.StatusNotFound, "score not found")
	default:
		h.writeError(w, err, fallback)
	}
}
