package routes

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterMediaRoutes mounts JWT-protected presigned upload/download routes.
func RegisterMediaRoutes(r chi.Router, media *handlers.MediaHandler, jwtSecret string, sellerActive func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(sellerActive)
		r.Post("/media/presign-upload", media.PresignUpload)
		r.Get("/media/url", media.GetURL)
	})
}
