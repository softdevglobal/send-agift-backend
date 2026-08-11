package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterCustomerRoutes mounts customer auth and profile/address routes.
func RegisterCustomerRoutes(r chi.Router, customers *handlers.CustomerHandler, jwtSecret string) {
	r.Post("/customers/register", customers.Register) // register a new customer	

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret)) // require authentication
		r.Use(middleware.RequireRole("customer")) // require role
		r.Get("/customers/me", customers.Me) // get the customer's profile
		r.Put("/customers/me", customers.UpdateMe) // update the customer's profile
		r.Delete("/customers/me", customers.DeleteMe) // delete the customer's profile
		r.Post("/customers/me/addresses", customers.AddAddress) // add a new address to the customer's profile
		r.Delete("/customers/me/addresses/{id}", customers.DeleteAddress) // delete an address from the customer's profile
	})
}
