package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterShippingRoutes mounts seller shipping and Shippo webhook routes.
func RegisterShippingRoutes(r chi.Router, shipping *handlers.ShippingHandler, jwtSecret string) {
	r.Post("/webhooks/shippo/tracking", shipping.ShippoWebhook)

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))
		r.Post("/sellers/me/order-items/{orderItemID}/shipping/rates", shipping.GetRates)
		r.Post("/sellers/me/order-items/{orderItemID}/shipping/labels", shipping.BuyLabel)
	})
}
