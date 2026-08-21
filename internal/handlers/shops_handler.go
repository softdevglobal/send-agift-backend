package handlers

import (
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"myapp/internal/services"
	"myapp/internal/utils"
)

// ShopsHandler exposes public marketplace browsing endpoints (no JWT required).
type ShopsHandler struct {
	marketplace *services.MarketplaceService
}

func NewShopsHandler(m *services.MarketplaceService) *ShopsHandler {
	return &ShopsHandler{marketplace: m}
}

// ListActiveShops returns only shops with status = 'active'.
func (h *ShopsHandler) ListActiveShops(w http.ResponseWriter, r *http.Request) {
	items, err := h.marketplace.ListActiveShops(r.Context())
	if err != nil {
		log.Printf("list active shops error: %v", err)
		utils.Error(w, http.StatusInternalServerError, "could not list shops")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

// ListShopProducts returns only published products for an active shop.
// Query param: customer_type=personal|corporate (optional; defaults to personal)
func (h *ShopsHandler) ListShopProducts(w http.ResponseWriter, r *http.Request) {
	shopID := chi.URLParam(r, "shopId")
	customerType := r.URL.Query().Get("customer_type")

	items, err := h.marketplace.ListPublishedProductsByShop(r.Context(), shopID, customerType)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalidCustomerType):
			utils.Error(w, http.StatusBadRequest, "customer_type must be personal or corporate")
		default:
			log.Printf("list shop products error: %v", err)
			utils.Error(w, http.StatusInternalServerError, "could not list products")
		}
		return
	}
	utils.JSON(w, http.StatusOK, items)
}
