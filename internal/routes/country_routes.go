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
		r.Use(middleware.RequireAuth(jwtSecret)) // require authentication
		r.Use(middleware.RequireRole("admin", "customer", "seller")) // require role
		r.Get("/countries", countries.List) // list the countries
		r.Get("/countries/{id}", countries.GetByID) // get the country by ID
	})

	// Write: admin / superadmin only
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret)) // require authentication
		r.Use(middleware.RequireRole("admin")) // require role
		r.Post("/admin/countries", countries.Create) // create a new country
		r.Put("/admin/countries/{id}", countries.Update) // update the country by ID
		r.Delete("/admin/countries/{id}", countries.Delete) // delete the country by ID
	})
}
