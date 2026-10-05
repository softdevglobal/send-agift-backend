package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterAuthRoutes mounts the shared login for admin, customer, and seller.
func RegisterAuthRoutes(r chi.Router, auth *handlers.AuthHandler, social *handlers.SocialAuthHandler, jwtSecret string) {
	// Customer sign-in with Google or Facebook, limited per address.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(20, time.Minute))
		r.Post("/auth/social", social.SignIn)
		r.Post("/auth/social/complete", social.Complete)
	})

	// A signed-in admin confirms their password again before a high-risk
	// action; limited per address so it cannot be used to guess passwords.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(10, time.Minute))
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Post("/admin/reauth", auth.Reauth)
	})

	r.Post("/auth/login", auth.Login)      // login for admin
	r.Post("/customers/login", auth.Login) // login for customer
	r.Post("/sellers/login", auth.Login)   // login for seller
}

// POST
// /api/v1/auth/login (and customer/seller login)
// { token, role }
