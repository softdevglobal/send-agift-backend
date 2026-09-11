package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterReelRoutes mounts the public customer reel feed and seller reel CRUD.
//
// A reel belongs to a shop. Tagging a product is optional:
//   - shop reel    → posted on the shop route with no product_id (shop's own video)
//   - product reel → posted on the product route (or with product_id in the body)
//
// Public (no JWT) — the TikTok-style feed (each item includes like_count,
// latest 3 recent_likers, and all visible comments):
//
//	GET /reels                    — newest published public reels (cursor paginated)
//	GET /reels/{id}               — one reel, counts a view
//	GET /shops/{shopId}/reels     — reels for one shop (?scope=shop for shop-only videos)
//	GET /products/{productId}/reels — reels for one product
//
// Seller-only (JWT + role=seller):
//
//	POST   /sellers/me/shops/{shopID}/reels
//	GET    /sellers/me/shops/{shopID}/reels
//	POST   /sellers/me/products/{productID}/reels
//	GET    /sellers/me/products/{productID}/reels
//	GET    /sellers/me/reels
//	GET    /sellers/me/reels/{id}
//	PUT    /sellers/me/reels/{id}
//	DELETE /sellers/me/reels/{id}
func RegisterReelRoutes(r chi.Router, reels *handlers.ReelHandler, jwtSecret string) {
	r.Get("/reels", reels.Feed)
	r.Get("/reels/{id}", reels.GetPublic)
	r.Get("/shops/{shopId}/reels", reels.FeedByShop)
	r.Get("/products/{productId}/reels", reels.FeedByProduct)

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))

		// Shop-level reels: product_id in the body is optional.
		r.Post("/sellers/me/shops/{shopID}/reels", reels.Create)
		r.Get("/sellers/me/shops/{shopID}/reels", reels.ListByShop)

		// Product-level reels: product taken from the URL.
		r.Post("/sellers/me/products/{productID}/reels", reels.CreateForProduct)
		r.Get("/sellers/me/products/{productID}/reels", reels.ListByProduct)

		r.Get("/sellers/me/reels", reels.ListMine)
		r.Get("/sellers/me/reels/{id}", reels.Get)
		r.Put("/sellers/me/reels/{id}", reels.Update)
		r.Delete("/sellers/me/reels/{id}", reels.Delete)
	})
}
