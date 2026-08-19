package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
)

// RegisterAdminRoutes mounts the one-time superadmin registration (bootstrap) route.
// POST /api/v1/admin/register
func RegisterAdminRoutes(r chi.Router, auth *handlers.AuthHandler) {
	r.Post("/admin/register", auth.Bootstrap) // register a new admin
}
