package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

// ReelHandler serves seller reel CRUD and the public customer reel feed.
type ReelHandler struct {
	reels *services.ReelService
}

func NewReelHandler(reels *services.ReelService) *ReelHandler {
	return &ReelHandler{reels: reels}
}

// Create handles POST /sellers/me/shops/{shopID}/reels.
func (h *ReelHandler) Create(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "shopID")
	var req services.ReelInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	details, err := h.reels.Create(r.Context(), sellerID, shopID, req)
	if err != nil {
		h.writeError(w, err, "could not create reel")
		return
	}
	utils.JSON(w, http.StatusCreated, details)
}

// CreateForProduct handles POST /sellers/me/products/{productID}/reels.
// The reel is tagged to that product; the shop comes from the product.
func (h *ReelHandler) CreateForProduct(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	productID := chi.URLParam(r, "productID")
	var req services.ReelInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	details, err := h.reels.CreateForProduct(r.Context(), sellerID, productID, req)
	if err != nil {
		h.writeError(w, err, "could not create reel")
		return
	}
	utils.JSON(w, http.StatusCreated, details)
}

// ListByProduct handles GET /sellers/me/products/{productID}/reels.
func (h *ReelHandler) ListByProduct(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	productID := chi.URLParam(r, "productID")
	items, err := h.reels.ListByProduct(r.Context(), sellerID, productID)
	if err != nil {
		h.writeError(w, err, "could not list reels")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

// ListByShop handles GET /sellers/me/shops/{shopID}/reels.
func (h *ReelHandler) ListByShop(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "shopID")
	items, err := h.reels.ListByShop(r.Context(), sellerID, shopID)
	if err != nil {
		h.writeError(w, err, "could not list reels")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

// ListMine handles GET /sellers/me/reels.
func (h *ReelHandler) ListMine(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	items, err := h.reels.ListBySeller(r.Context(), sellerID)
	if err != nil {
		h.writeError(w, err, "could not list reels")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

// Get handles GET /sellers/me/reels/{id} (any status, owner only).
func (h *ReelHandler) Get(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	reelID := chi.URLParam(r, "id")
	details, err := h.reels.Get(r.Context(), sellerID, reelID)
	if err != nil {
		h.writeError(w, err, "could not get reel")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// Update handles PUT /sellers/me/reels/{id}.
func (h *ReelHandler) Update(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	reelID := chi.URLParam(r, "id")
	var req services.ReelInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	details, err := h.reels.Update(r.Context(), sellerID, reelID, req)
	if err != nil {
		h.writeError(w, err, "could not update reel")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// Delete handles DELETE /sellers/me/reels/{id}.
func (h *ReelHandler) Delete(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	reelID := chi.URLParam(r, "id")
	if err := h.reels.Delete(r.Context(), sellerID, reelID); err != nil {
		h.writeError(w, err, "could not delete reel")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "reel deleted"})
}

// Feed handles GET /reels — the public customer feed.
// Query params: shop_id, product_id, scope (all|shop|product), limit (max 50), cursor.
func (h *ReelHandler) Feed(w http.ResponseWriter, r *http.Request) {
	h.writeFeed(w, r, "", "")
}

// FeedByShop handles GET /shops/{shopId}/reels — public reels for one shop.
// Add ?scope=shop for the shop's own videos only (no product tagged).
func (h *ReelHandler) FeedByShop(w http.ResponseWriter, r *http.Request) {
	h.writeFeed(w, r, chi.URLParam(r, "shopId"), "")
}

// FeedByProduct handles GET /products/{productId}/reels — public reels for one product.
func (h *ReelHandler) FeedByProduct(w http.ResponseWriter, r *http.Request) {
	h.writeFeed(w, r, "", chi.URLParam(r, "productId"))
}

// writeFeed reads shared feed query params, letting path params win over the query string.
func (h *ReelHandler) writeFeed(w http.ResponseWriter, r *http.Request, shopID, productID string) {
	q := r.URL.Query()
	if shopID == "" {
		shopID = q.Get("shop_id")
	}
	if productID == "" {
		productID = q.Get("product_id")
	}

	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			utils.Error(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = n
	}

	feed, err := h.reels.Feed(r.Context(), services.ReelFeedFilter{
		ShopID:    shopID,
		ProductID: productID,
		Scope:     q.Get("scope"),
		Cursor:    q.Get("cursor"),
		Limit:     limit,
	})
	if err != nil {
		h.writeError(w, err, "could not list reels")
		return
	}
	utils.JSON(w, http.StatusOK, feed)
}

// GetPublic handles GET /reels/{id} — published public reel, counts a view.
func (h *ReelHandler) GetPublic(w http.ResponseWriter, r *http.Request) {
	reelID := chi.URLParam(r, "id")
	details, err := h.reels.GetPublic(r.Context(), reelID)
	if err != nil {
		h.writeError(w, err, "could not get reel")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// writeError maps reel service errors to HTTP status codes.
func (h *ReelHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidReel):
		utils.Error(w, http.StatusBadRequest, "media[] requires object_path and an image/* or video/* mime_type; visibility public|private; status draft|published|archived")
	case errors.Is(err, services.ErrReelProduct):
		utils.Error(w, http.StatusBadRequest, "product_id must belong to this shop")
	case errors.Is(err, services.ErrInvalidCursor):
		utils.Error(w, http.StatusBadRequest, "invalid cursor")
	case errors.Is(err, services.ErrInvalidReelScope):
		utils.Error(w, http.StatusBadRequest, "scope must be all, shop, or product")
	case errors.Is(err, services.ErrShopNotFound):
		utils.Error(w, http.StatusNotFound, "shop not found")
	case errors.Is(err, services.ErrProductNotFound):
		utils.Error(w, http.StatusNotFound, "product not found")
	case errors.Is(err, services.ErrReelNotFound):
		utils.Error(w, http.StatusNotFound, "reel not found")
	default:
		log.Printf("reel handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
