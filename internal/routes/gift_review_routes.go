package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterGiftReviewRoutes mounts the review link a gift recipient opens from
// their email or text. The token in the path is the link; no session needed.
//
//	GET  /gift-reviews/{token}          the gift behind the link
//	POST /gift-reviews/{token}/code     send a one-time code to the recipient
//	POST /gift-reviews/{token}/verify   check the code; signs in, making an account if needed
func RegisterGiftReviewRoutes(r chi.Router, reviews *handlers.GiftReviewHandler) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(10, time.Minute))
		r.Get("/gift-reviews/{token}", reviews.Preview)
		r.Post("/gift-reviews/{token}/code", reviews.RequestCode)
		r.Post("/gift-reviews/{token}/verify", reviews.Verify)
	})
}
