package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterShippingRoutes mounts Shippo shipping endpoints.
//
// Public:
//   POST /webhooks/shippo/tracking — Shippo tracking updates (no JWT)
//
// Seller-only (JWT + role=seller):
//   POST .../shipping/rates  — quote rates; stores parcel/customs on marketplace.shipments
//   POST .../shipping/labels — buy label for a chosen rate_object_id
//   GET  .../shipping/label   — short-lived download link for the bought label PDF
//   POST .../shipping/manual  — record a seller-arranged shipment when no carrier quotes the lane
func RegisterShippingRoutes(r chi.Router, shipping *handlers.ShippingHandler, jwtSecret string) {
	// Called by Shippo when tracking status changes (track_updated).
	r.Post("/webhooks/shippo/tracking", shipping.ShippoWebhook)

	// Customers price delivery at checkout, before any order exists.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))

		// Body: recipient_id, delivery_date, items[{product_id, quantity}].
		r.Post("/customers/me/shipping/quote", shipping.QuoteDelivery)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))

		// Body may include parcel + customs_declaration (required for international).
		r.Post("/sellers/me/order-items/{orderItemID}/shipping/rates", shipping.GetRates)
		// Body: rate_object_id, provider, idempotency_key. No customs here — already saved at rates.
		r.Post("/sellers/me/order-items/{orderItemID}/shipping/labels", shipping.BuyLabel)
		// Label PDFs live in a private bucket; this hands back a presigned link.
		r.Get("/sellers/me/order-items/{orderItemID}/shipping/label", shipping.LabelURL)
		// Body: courier_provider, tracking_number, tracking_url (optional).
		// Fallback when GetRates returns no rates for the lane at all.
		r.Post("/sellers/me/order-items/{orderItemID}/shipping/manual", shipping.MarkShippedManually)

	    // Start local delivery// Place these inside the seller-authenticated group so the JWT middleware
	    // runs first and sets UserIDContextKey.
		r.Post("/sellers/me/order-items/{orderItemID}/shipping/local", shipping.StartLocalDelivery)              
		r.Post("/sellers/me/order-items/{orderItemID}/shipping/local/delivered", shipping.CompleteLocalDelivery) 
	})
}
