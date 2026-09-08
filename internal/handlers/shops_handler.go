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

// GetShop returns one active shop — the public shop page header.
func (h *ShopsHandler) GetShop(w http.ResponseWriter, r *http.Request) {
	shop, err := h.marketplace.GetActiveShop(r.Context(), chi.URLParam(r, "shopId"))
	if err != nil {
		h.writeError(w, err, "could not get shop")
		return
	}
	utils.JSON(w, http.StatusOK, shop)
}

// ListShopProducts returns only published products for an active shop.
// Query param: customer_type=personal|corporate (optional; defaults to personal)
func (h *ShopsHandler) ListShopProducts(w http.ResponseWriter, r *http.Request) {
	shopID := chi.URLParam(r, "shopId")
	customerType := r.URL.Query().Get("customer_type")

	items, err := h.marketplace.ListPublishedProductsByShop(r.Context(), shopID, customerType)
	if err != nil {
		h.writeError(w, err, "could not list products")
		return
	}
	utils.JSON(w, http.StatusOK, items)
}

// GetProduct returns one published product plus its shop — the public product page.
// Query param: customer_type=personal|corporate (optional; defaults to personal)
func (h *ShopsHandler) GetProduct(w http.ResponseWriter, r *http.Request) {
	productID := chi.URLParam(r, "productId")
	customerType := r.URL.Query().Get("customer_type")

	product, err := h.marketplace.GetPublishedProduct(r.Context(), productID, customerType)
	if err != nil {
		h.writeError(w, err, "could not get product")
		return
	}
	utils.JSON(w, http.StatusOK, product)
}

// writeError maps marketplace service errors to HTTP status codes.
// Unpublished products and inactive shops surface as 404, never as a hint that they exist.
func (h *ShopsHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidCustomerType):
		utils.Error(w, http.StatusBadRequest, "customer_type must be personal or corporate")
	case errors.Is(err, services.ErrShopNotFound):
		utils.Error(w, http.StatusNotFound, "shop not found")
	case errors.Is(err, services.ErrProductNotFound):
		utils.Error(w, http.StatusNotFound, "product not found")
	default:
		log.Printf("marketplace error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
