package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
)

// RegisterMarketplaceRoutes mounts customer-facing, read-only marketplace browsing routes.
// These endpoints are public (no JWT required).
func RegisterMarketplaceRoutes(r chi.Router, shops *handlers.ShopsHandler) {
	r.Get("/shops", shops.ListActiveShops)
	r.Get("/shops/{shopId}/products", shops.ListShopProducts)
}

