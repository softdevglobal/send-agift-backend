package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
)

// RegisterAvailabilityRoutes mounts the public Find gifts search.
//
//	GET /availability?latitude=&longitude=&delivery_date=
//
// delivery_date is optional (yyyy-mm-dd). The result is the shops whose
// delivery zones cover that point and can arrive in time, with the gift ids
// that are in stock for the day.
func RegisterAvailabilityRoutes(r chi.Router, availability *handlers.AvailabilityHandler) {
	r.Get("/availability", availability.Search)
}
