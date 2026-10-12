package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterLoginCodeRoutes mounts customer sign-in by SMS or email code, and
// phone verification for a signed-in customer.
func RegisterLoginCodeRoutes(r chi.Router, codes *handlers.LoginCodeHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(10, time.Minute))
		r.Post("/customers/login/code", codes.RequestCode)
		r.Post("/customers/login/code/verify", codes.VerifyCode)
	})
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(10, time.Minute))
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Post("/customers/me/phone/code", codes.RequestPhoneCode)
		r.Post("/customers/me/phone/verify", codes.VerifyPhone)
	})
}
