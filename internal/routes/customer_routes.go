package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterCustomerRoutes mounts customer auth and profile/address routes.
func RegisterCustomerRoutes(r chi.Router, customers *handlers.CustomerHandler, jwtSecret string) {
	r.Post("/customers/register", customers.Register)

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Get("/customers/me", customers.Me)
		r.Put("/customers/me", customers.UpdateMe)
		r.Delete("/customers/me", customers.DeleteMe)
		r.Post("/customers/me/addresses", customers.AddAddress)
		r.Delete("/customers/me/addresses/{id}", customers.DeleteAddress)
	})
}
