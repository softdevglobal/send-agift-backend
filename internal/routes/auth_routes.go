package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
)

// RegisterAuthRoutes mounts the shared login for admin, customer, and seller.
func RegisterAuthRoutes(r chi.Router, auth *handlers.AuthHandler) {
	r.Post("/auth/login", auth.Login)
	r.Post("/customers/login", auth.Login)
	r.Post("/sellers/login", auth.Login)
}
