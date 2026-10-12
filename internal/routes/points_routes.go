package routes

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterSellerPointsRoutes mounts sellers buying points.
//
// Seller JWT:
//
//	GET  /sellers/me/points                                    . Balance, reserved, rate, history
//	GET  /sellers/me/points/purchases                          . Purchase history
//	POST /sellers/me/points/purchases                          . Start a purchase (Idempotency-Key)
//	POST /sellers/me/points/purchases/{purchaseID}/cancel      . Abandon a pending purchase
//	POST /sellers/me/points/purchases/{purchaseID}/test-payment. Test provider only
//
// Payment provider (HMAC-signed, no JWT):
//
//	POST /payments/points/webhook
//
// Packages: sellers read GET /sellers/me/points/packages; admins read
// GET /admin/points/packages; a Super Admin creates and edits them with
// POST /admin/points/packages and PUT /admin/points/packages/{packageID}.
// With the Stripe provider, POST /sellers/me/points/purchases/{id}/sync
// re-checks a payment when the seller comes back from Stripe.
//
// Admin JWT views purchases; a Super Admin with a fresh password
// confirmation (X-Reauth-Token) confirms or fails them, which credits or
// refuses the points.
func RegisterSellerPointsRoutes(r chi.Router, h *handlers.SellerPointsHandler, jwtSecret string, sellerActive func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))
		r.Use(sellerActive)
		r.Get("/sellers/me/points", h.Wallet)
		r.Get("/sellers/me/points/purchases", h.Purchases)
		r.Get("/sellers/me/points/packages", h.Packages)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimitByUser(30, time.Minute))
			r.Post("/sellers/me/points/purchases", h.CreatePurchase)
			r.Post("/sellers/me/points/purchases/{purchaseID}/cancel", h.CancelPurchase)
			r.Post("/sellers/me/points/purchases/{purchaseID}/test-payment", h.TestPayment)
			r.Post("/sellers/me/points/purchases/{purchaseID}/sync", h.SyncPurchase)
		})
	})

	r.Post("/payments/points/webhook", h.Webhook)

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Get("/admin/points/purchases", h.AdminPurchases)
		r.Get("/admin/points/packages", h.AdminPackages)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("superadmin"))
		r.Use(middleware.RequireReauth(jwtSecret))
		r.Post("/admin/points/purchases/{purchaseID}/confirm", h.AdminConfirm)
		r.Post("/admin/points/purchases/{purchaseID}/fail", h.AdminFail)
		r.Post("/admin/points/packages", h.AdminCreatePackage)
		r.Put("/admin/points/packages/{packageID}", h.AdminUpdatePackage)
	})
}
