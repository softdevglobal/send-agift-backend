package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterPlacesRoutes mounts the Google Places proxy.
// These are public: address pickers are used on register/checkout forms that
// run before a customer or seller has a token.
// Because they are open, they sit behind a per-IP rate limit so nobody can
// run up the Google bill by hammering them.
func RegisterPlacesRoutes(r chi.Router, places *handlers.PlacesHandler) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(120, time.Minute))
		r.Get("/places/autocomplete", places.Autocomplete)
		r.Get("/places/details", places.Details)
	})
}
