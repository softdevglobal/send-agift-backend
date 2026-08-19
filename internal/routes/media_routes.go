package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterMediaRoutes mounts JWT-protected presigned upload/download routes.
func RegisterMediaRoutes(r chi.Router, media *handlers.MediaHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Post("/media/presign-upload", media.PresignUpload)
		r.Get("/media/url", media.GetURL)
	})
}
