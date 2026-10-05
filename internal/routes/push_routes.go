package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterPushRoutes mounts the device registration the mobile app uses for
// push notifications (customer JWT):
//
//	POST   /customers/me/push-devices. Register this device's Firebase token
//	DELETE /customers/me/push-devices. Forget it, on sign-out
//	GET    /customers/me/notifications. The in-app inbox, with the unread count
//	POST   /customers/me/notifications/read. Mark some (ids) or all read
func RegisterPushRoutes(r chi.Router, push *handlers.PushHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Post("/customers/me/push-devices", push.RegisterDevice)
		r.Delete("/customers/me/push-devices", push.UnregisterDevice)
		r.Get("/customers/me/notifications", push.Inbox)
		r.Post("/customers/me/notifications/read", push.MarkRead)
	})
}
