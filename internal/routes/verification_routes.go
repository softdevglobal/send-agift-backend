package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterVerificationRoutes mounts seller email confirmation and the admin
// seller review queue.
//
// Public (rate limited per address):
//
//	POST /sellers/verify-email          { email, code } → { token, role, verification_status }
//	POST /sellers/verify-email/resend   { email }
//
// Admin (JWT + role=admin):
//
//	GET   /admin/sellers?status=&q=&limit=&offset=
//	GET   /admin/sellers/{id}
//	PATCH /admin/sellers/{id}/verification   { status: verified|rejected, note }
func RegisterVerificationRoutes(r chi.Router, v *handlers.VerificationHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(10, time.Minute))
		r.Post("/sellers/verify-email", v.VerifyEmail)
		r.Post("/sellers/verify-email/resend", v.ResendCode)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Get("/admin/sellers", v.AdminList)
		r.Get("/admin/sellers/{id}", v.AdminGet)
		r.Patch("/admin/sellers/{id}/verification", v.AdminReview)
	})
}
