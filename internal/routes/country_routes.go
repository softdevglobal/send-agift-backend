package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterCountryRoutes mounts public country read, admin country write, and admin capability CRUD.
func RegisterCountryRoutes(
	r chi.Router,
	countries *handlers.CountryHandler,
	capabilities *handlers.CountryCapabilityHandler,
	jwtSecret string,
) {
	// Read: public (no JWT)
	r.Get("/countries", countries.List)
	r.Get("/countries/{id}", countries.GetByID)

	// Admin: countries + country capabilities
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))

		r.Post("/admin/countries", countries.Create)
		r.Put("/admin/countries/{id}", countries.Update)
		r.Delete("/admin/countries/{id}", countries.Delete)

		r.Get("/admin/country-capabilities", capabilities.List)
		r.Get("/admin/countries/{id}/capabilities", capabilities.GetByCountryID)
		r.Post("/admin/countries/{id}/capabilities", capabilities.Create)
		r.Put("/admin/countries/{id}/capabilities", capabilities.Update)
		r.Delete("/admin/countries/{id}/capabilities", capabilities.Delete)
	})
}
