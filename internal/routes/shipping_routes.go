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
// Seller-only (JWT + role=seller), under /sellers/me/orders/{orderID}/shops/{shopID}:
//   POST .../shipping/rates  — quote rates; stores parcel/customs on marketplace.shipments
//   POST .../shipping/labels — buy label for a chosen rate_object_id
//   GET  .../shipping/label   — short-lived download link for the bought label PDF
//   POST .../shipping/manual  — record a seller-arranged shipment when no carrier quotes the lane
//   POST .../shipping/local   — start shop delivery; .../local/delivered completes it
func RegisterShippingRoutes(r chi.Router, shipping *handlers.ShippingHandler, jwtSecret string) {
	// Called by Shippo when tracking status changes (track_updated).
	r.Post("/webhooks/shippo/tracking", shipping.ShippoWebhook)

	// Customers price delivery at checkout, before any order exists.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))

		// Body: recipient_id, delivery_date, items[{product_id, quantity}].
		// Response: shops[].options with days_available (AliExpress-style) + shipments[] recommended.
		r.Post("/customers/me/shipping/quote", shipping.QuoteDelivery)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))

		// Every product from {shopID} on {orderID} is quoted, labelled and
		// dispatched together. An order with two shops is two parcels, so each
		// shop has its own URL.

		// Body may include parcel + customs_declaration (required for international).
		r.Post("/sellers/me/orders/{orderID}/shops/{shopID}/shipping/rates", shipping.GetRates)

		// Body: rate_object_id, provider, idempotency_key. No customs here — already saved at rates.
		r.Post("/sellers/me/orders/{orderID}/shops/{shopID}/shipping/labels", shipping.BuyLabel)

		// Label PDFs live in a private bucket; this hands back a presigned link.
		r.Get("/sellers/me/orders/{orderID}/shops/{shopID}/shipping/label", shipping.LabelURL)

		// Body: courier_provider, tracking_number, tracking_url (optional), estimated_days (optional).
		// Fallback when rates returns no carrier for the lane at all.
		r.Post("/sellers/me/orders/{orderID}/shops/{shopID}/shipping/manual", shipping.MarkShippedManually)

		// Shop delivers the parcel itself; only allowed inside the shop's delivery zones.
		r.Post("/sellers/me/orders/{orderID}/shops/{shopID}/shipping/local", shipping.StartLocalDelivery)

		// No body. Marks the shop's local delivery on this order as delivered.
		r.Post("/sellers/me/orders/{orderID}/shops/{shopID}/shipping/local/delivered", shipping.CompleteLocalDelivery)
	})
}
