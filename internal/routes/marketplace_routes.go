package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
)

// RegisterMarketplaceRoutes mounts customer-facing, read-only marketplace browsing routes.
// These endpoints are public (no JWT required) and only ever expose active shops
// and published products.
//
//	GET /shops                       — all active shops
//	GET /shops/{shopId}              — one shop (storefront header)
//	GET /shops/{shopId}/products     — that shop's published products
//	GET /products/{productId}        — one product + its shop (product page)
//
// The matching reel feeds (/shops/{shopId}/reels, /products/{productId}/reels)
// are registered in RegisterReelRoutes.
func RegisterMarketplaceRoutes(r chi.Router, shops *handlers.ShopsHandler) {
	r.Get("/shops", shops.ListActiveShops)
	r.Get("/shops/{shopId}", shops.GetShop)
	r.Get("/shops/{shopId}/products", shops.ListShopProducts)
	r.Get("/products/{productId}", shops.GetProduct)
}

