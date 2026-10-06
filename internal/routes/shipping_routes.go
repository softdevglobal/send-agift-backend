package routes

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterShippingRoutes mounts zone delivery and shop hand-over.
//
// Customer (JWT + role=customer):
//
//	POST /customers/me/shipping/quote. Price each shop from its delivery zones
//
// Seller (JWT + role=seller), under /sellers/me/orders/{orderID}/shops/{shopID}:
//
//	POST .../shipping/local           . Start shop delivery inside the zones
//	POST .../shipping/local/delivered . Mark that hand-over complete
func RegisterShippingRoutes(r chi.Router, shipping *handlers.ShippingHandler, jwtSecret string, sellerActive func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Post("/customers/me/shipping/quote", shipping.QuoteDelivery)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))
		r.Use(sellerActive)
		r.Post("/sellers/me/orders/{orderID}/shops/{shopID}/shipping/local", shipping.StartLocalDelivery)
		r.Post("/sellers/me/orders/{orderID}/shops/{shopID}/shipping/local/delivered", shipping.CompleteLocalDelivery)
	})
}
