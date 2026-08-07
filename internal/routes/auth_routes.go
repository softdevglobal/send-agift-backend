package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
)

// RegisterAuthRoutes mounts public auth endpoints (login).
// POST /api/v1/auth/login
func RegisterAuthRoutes(r chi.Router, auth *handlers.AuthHandler) {
	r.Route("/auth", func(r chi.Router) {
		r.Post("/login", auth.Login)
	})
}
