package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterProductReviewRoutes mounts AliExpress-style product reviews.
//
// Public (no JWT; optional customer JWT sets voted_helpful):
//
//	GET /products/{productId}/reviews
//	GET /products/{productId}/reviews/summary
//	GET /shops/{shopId}/reviews
//	GET /reviews/{id}
//
// Customer (JWT + role=customer):
//
//	POST   /customers/me/order-items/{orderItemId}/reviews
//	GET    /customers/me/reviews
//	GET    /customers/me/reviews/{id}
//	PUT    /customers/me/reviews/{id}
//	DELETE /customers/me/reviews/{id}
//	PUT    /reviews/{id}/vote
//	DELETE /reviews/{id}/vote
//
// Seller (JWT + role=seller):
//
//	GET    /sellers/me/reviews
//	GET    /sellers/me/reviews/{id}
//	PUT    /sellers/me/reviews/{id}/reply
//	DELETE /sellers/me/reviews/{id}/reply
func RegisterProductReviewRoutes(r chi.Router, reviews *handlers.ProductReviewHandler, jwtSecret string) {
	r.Get("/products/{productId}/reviews/summary", reviews.SummaryByProduct)
	r.Get("/products/{productId}/reviews", reviews.ListByProduct)
	r.Get("/shops/{shopId}/reviews", reviews.ListByShop)
	r.Get("/reviews/{id}", reviews.GetPublic)

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))

		r.Post("/customers/me/order-items/{orderItemId}/reviews", reviews.Create)
		r.Get("/customers/me/reviews", reviews.ListMine)
		r.Get("/customers/me/reviews/{id}", reviews.GetMine)
		r.Put("/customers/me/reviews/{id}", reviews.Update)
		r.Delete("/customers/me/reviews/{id}", reviews.Delete)

		r.Put("/reviews/{id}/vote", reviews.Vote)
		r.Delete("/reviews/{id}/vote", reviews.ClearVote)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))

		r.Get("/sellers/me/reviews", reviews.ListForSeller)
		r.Get("/sellers/me/reviews/{id}", reviews.GetForSeller)
		r.Put("/sellers/me/reviews/{id}/reply", reviews.Reply)
		r.Delete("/sellers/me/reviews/{id}/reply", reviews.ClearReply)
	})
}
