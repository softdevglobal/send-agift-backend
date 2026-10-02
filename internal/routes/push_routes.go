package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterPushRoutes mounts the device registration the mobile app uses for
// push notifications (customer JWT):
//
//	POST   /customers/me/push-devices — register this device's Firebase token
//	DELETE /customers/me/push-devices — forget it, on sign-out
func RegisterPushRoutes(r chi.Router, push *handlers.PushHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Post("/customers/me/push-devices", push.RegisterDevice)
		r.Delete("/customers/me/push-devices", push.UnregisterDevice)
	})
}
