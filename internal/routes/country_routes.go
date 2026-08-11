package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterCountryRoutes mounts country read (auth roles) and admin write routes.
func RegisterCountryRoutes(r chi.Router, countries *handlers.CountryHandler, jwtSecret string) {
	// Read: admin, customer, or seller only
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin", "customer", "seller"))
		r.Get("/countries", countries.List)
		r.Get("/countries/{id}", countries.GetByID)
	})

	// Write: admin / superadmin only
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Post("/admin/countries", countries.Create)
		r.Put("/admin/countries/{id}", countries.Update)
		r.Delete("/admin/countries/{id}", countries.Delete)
	})
}
