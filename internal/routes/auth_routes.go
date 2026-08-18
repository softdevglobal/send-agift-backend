package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
)

// RegisterAuthRoutes mounts the shared login for admin, customer, and seller.
func RegisterAuthRoutes(r chi.Router, auth *handlers.AuthHandler) {
	r.Post("/auth/login", auth.Login) // login for admin
	r.Post("/customers/login", auth.Login) // login for customer
	r.Post("/sellers/login", auth.Login) // login for seller	
}

// POST
// /api/v1/auth/login (and customer/seller login)
// { token, role }