package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterSellerPointsRoutes mounts sellers buying points.
//
// Seller JWT:
//
//	GET  /sellers/me/points                                     — balance, reserved, rate, history
//	GET  /sellers/me/points/purchases                           — purchase history
//	POST /sellers/me/points/purchases                           — start a purchase (Idempotency-Key)
//	POST /sellers/me/points/purchases/{purchaseID}/cancel       — abandon a pending purchase
//	POST /sellers/me/points/purchases/{purchaseID}/test-payment — test provider only
//
// Payment provider (HMAC-signed, no JWT):
//
//	POST /payments/points/webhook
//
// Admin JWT views purchases; a Super Admin with a fresh password
// confirmation (X-Reauth-Token) confirms or fails them, which credits or
// refuses the points.
func RegisterSellerPointsRoutes(r chi.Router, h *handlers.SellerPointsHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))
		r.Get("/sellers/me/points", h.Wallet)
		r.Get("/sellers/me/points/purchases", h.Purchases)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimitByUser(30, time.Minute))
			r.Post("/sellers/me/points/purchases", h.CreatePurchase)
			r.Post("/sellers/me/points/purchases/{purchaseID}/cancel", h.CancelPurchase)
			r.Post("/sellers/me/points/purchases/{purchaseID}/test-payment", h.TestPayment)
		})
	})

	r.Post("/payments/points/webhook", h.Webhook)

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Get("/admin/points/purchases", h.AdminPurchases)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("superadmin"))
		r.Use(middleware.RequireReauth(jwtSecret))
		r.Post("/admin/points/purchases/{purchaseID}/confirm", h.AdminConfirm)
		r.Post("/admin/points/purchases/{purchaseID}/fail", h.AdminFail)
	})
}
