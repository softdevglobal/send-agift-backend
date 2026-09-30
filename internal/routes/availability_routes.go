package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
)

// RegisterAvailabilityRoutes mounts the public catalog delivery check.
//
//	POST /shipping/availability
//
// Body: delivery_date (YYYY-MM-DD) and destination (street, city, country, latitude, longitude).
// One product per shop is quoted. Shops that can arrive by the date are available=true.
func RegisterAvailabilityRoutes(r chi.Router, availability *handlers.AvailabilityHandler) {
	r.Post("/shipping/availability", availability.Check)
}
