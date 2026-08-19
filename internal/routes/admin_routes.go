package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterAdminProtectedRoutes mounts JWT-protected admin endpoints.
func RegisterAdminProtectedRoutes(r chi.Router, admin *handlers.AdminHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Get("/admin/me", admin.Me)
		r.Put("/admin/me", admin.UpdateMe)
	})
}
