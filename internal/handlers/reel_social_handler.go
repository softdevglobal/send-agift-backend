package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// ReelSocialHandler serves public reel likes and comments.
// Same URLs for guests (X-Guest-Token) and logged-in customers (JWT).
type ReelSocialHandler struct {
	social    *services.ReelSocialService
	jwtSecret string // used only for optional liked_by_requester on public GET likes
}

func NewReelSocialHandler(social *services.ReelSocialService, jwtSecret string) *ReelSocialHandler {
	return &ReelSocialHandler{social: social, jwtSecret: jwtSecret}
}

// actorFromContext builds SocialActor from OptionalCustomerOrGuest middleware.
func actorFromContext(r *http.Request) services.SocialActor {
	var a services.SocialActor
	if uid, ok := r.Context().Value(middleware.UserIDContextKey).(string); ok {
		a.CustomerID = uid
	}
	if guest, ok := r.Context().Value(middleware.GuestTokenContextKey).(string); ok {
		a.GuestToken = guest
	}
	return a
}

// optionalActorFromHeaders reads JWT/guest without requiring them (public GET likes).
func (h *ReelSocialHandler) optionalActorFromHeaders(r *http.Request) services.SocialActor {
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, "Bearer ") {
		tokenStr := strings.TrimPrefix(header, "Bearer ")
		if claims, err := utils.ParseJWT(tokenStr, h.jwtSecret); err == nil && claims.Role == "customer" {
			return services.SocialActor{CustomerID: claims.Subject}
		}
	}
	guest := strings.TrimSpace(r.Header.Get(middleware.GuestTokenHeader))
	if guest != "" {
		if _, err := uuid.Parse(guest); err == nil {
			return services.SocialActor{GuestToken: guest}
		}
	}
	return services.SocialActor{}
}

// GetLikes handles GET /reels/{id}/likes — fully public (like_count + recent_likers).
// Optional JWT / X-Guest-Token only affects liked_by_requester.
func (h *ReelSocialHandler) GetLikes(w http.ResponseWriter, r *http.Request) {
	reelID := chi.URLParam(r, "id")
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			utils.Error(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = n
	}
	res, err := h.social.GetLikes(r.Context(), reelID, h.optionalActorFromHeaders(r), limit)
	if err != nil {
		h.writeError(w, err, "could not get likes")
		return
	}
	utils.JSON(w, http.StatusOK, res)
}

// Like handles POST /reels/{id}/likes
func (h *ReelSocialHandler) Like(w http.ResponseWriter, r *http.Request) {
	reelID := chi.URLParam(r, "id")
	res, err := h.social.Like(r.Context(), reelID, actorFromContext(r))
	if err != nil {
		h.writeError(w, err, "could not like reel")
		return
	}
	utils.JSON(w, http.StatusOK, res)
}

// Unlike handles DELETE /reels/{id}/likes
func (h *ReelSocialHandler) Unlike(w http.ResponseWriter, r *http.Request) {
	reelID := chi.URLParam(r, "id")
	res, err := h.social.Unlike(r.Context(), reelID, actorFromContext(r))
	if err != nil {
		h.writeError(w, err, "could not unlike reel")
		return
	}
	utils.JSON(w, http.StatusOK, res)
}

// LikedByMe handles GET /reels/{id}/likes/me
func (h *ReelSocialHandler) LikedByMe(w http.ResponseWriter, r *http.Request) {
	reelID := chi.URLParam(r, "id")
	res, err := h.social.LikedByMe(r.Context(), reelID, actorFromContext(r))
	if err != nil {
		h.writeError(w, err, "could not check like")
		return
	}
	utils.JSON(w, http.StatusOK, res)
}

// ListComments handles GET /reels/{id}/comments (public, no auth).
func (h *ReelSocialHandler) ListComments(w http.ResponseWriter, r *http.Request) {
	reelID := chi.URLParam(r, "id")
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			utils.Error(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = n
	}
	list, err := h.social.ListComments(r.Context(), reelID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.writeError(w, err, "could not list comments")
		return
	}
	utils.JSON(w, http.StatusOK, list)
}

// CreateComment handles POST /reels/{id}/comments
func (h *ReelSocialHandler) CreateComment(w http.ResponseWriter, r *http.Request) {
	reelID := chi.URLParam(r, "id")
	var req services.CommentInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	view, err := h.social.CreateComment(r.Context(), reelID, actorFromContext(r), req)
	if err != nil {
		h.writeError(w, err, "could not create comment")
		return
	}
	utils.JSON(w, http.StatusCreated, view)
}

// UpdateComment handles PUT /reels/{id}/comments/{commentId}
// Author only: same customer JWT or same X-Guest-Token used when posting.
func (h *ReelSocialHandler) UpdateComment(w http.ResponseWriter, r *http.Request) {
	reelID := chi.URLParam(r, "id")
	commentID := chi.URLParam(r, "commentId")
	var req services.CommentInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	view, err := h.social.UpdateComment(r.Context(), reelID, commentID, actorFromContext(r), req)
	if err != nil {
		h.writeError(w, err, "could not update comment")
		return
	}
	utils.JSON(w, http.StatusOK, view)
}

// DeleteComment handles DELETE /reels/{id}/comments/{commentId}
func (h *ReelSocialHandler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	reelID := chi.URLParam(r, "id")
	commentID := chi.URLParam(r, "commentId")
	if err := h.social.DeleteComment(r.Context(), reelID, commentID, actorFromContext(r)); err != nil {
		h.writeError(w, err, "could not delete comment")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "comment deleted"})
}

func (h *ReelSocialHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrSocialIdentityRequired):
		utils.Error(w, http.StatusUnauthorized, "provide customer Bearer token or X-Guest-Token")
	case errors.Is(err, services.ErrInvalidComment):
		utils.Error(w, http.StatusBadRequest, "body required, max 1000 characters")
	case errors.Is(err, services.ErrInvalidCursor):
		utils.Error(w, http.StatusBadRequest, "invalid cursor")
	case errors.Is(err, services.ErrReelNotPublicSocial):
		utils.Error(w, http.StatusNotFound, "reel not found")
	case errors.Is(err, services.ErrCommentNotFound):
		utils.Error(w, http.StatusNotFound, "comment not found")
	case errors.Is(err, services.ErrReelLikeNotFound):
		utils.Error(w, http.StatusNotFound, "like not found")
	default:
		log.Printf("reel social handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
