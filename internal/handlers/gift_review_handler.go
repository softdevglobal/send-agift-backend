package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"myapp/internal/services"
	"myapp/internal/utils"
)

// GiftReviewHandler serves the page a gift recipient reaches from the link in
// their email or text. Nothing here needs a session: the link and a one-time
// code are the proof.
type GiftReviewHandler struct {
	reviews *services.GiftReviewService
}

func NewGiftReviewHandler(reviews *services.GiftReviewService) *GiftReviewHandler {
	return &GiftReviewHandler{reviews: reviews}
}

// Preview handles GET /gift-reviews/{token}.
func (h *GiftReviewHandler) Preview(w http.ResponseWriter, r *http.Request) {
	out, err := h.reviews.Preview(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeGiftReviewError(w, err)
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

// RequestCode handles POST /gift-reviews/{token}/code.
func (h *GiftReviewHandler) RequestCode(w http.ResponseWriter, r *http.Request) {
	if err := h.reviews.RequestCode(r.Context(), chi.URLParam(r, "token")); err != nil {
		writeGiftReviewError(w, err)
		return
	}
	utils.JSON(w, http.StatusAccepted, map[string]string{"message": "A code is on its way."})
}

// Verify handles POST /gift-reviews/{token}/verify.
func (h *GiftReviewHandler) Verify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	out, err := h.reviews.Verify(r.Context(), chi.URLParam(r, "token"), req.Code)
	if err != nil {
		writeGiftReviewError(w, err)
		return
	}
	utils.JSON(w, http.StatusOK, out)
}

func writeGiftReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, services.ErrGiftReviewLink):
		utils.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, services.ErrGiftReviewAccount):
		utils.Error(w, http.StatusConflict, err.Error())
	default:
		writeLoginCodeError(w, err, "could not open this review link")
	}
}
