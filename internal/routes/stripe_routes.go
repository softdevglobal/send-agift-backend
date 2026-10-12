package routes

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterStripeRoutes mounts card payments and seller payouts.
//
// Customer JWT:
//
//	POST /customers/me/orders/{id}/pay           . Start paying an order; returns Stripe's payment page
//	POST /customers/me/orders/{id}/payment/sync  . Re-check the payment with Stripe (webhook missed)
//
// Seller JWT:
//
//	GET  /sellers/me/stripe              . Payout account status
//	GET  /sellers/me/payouts             . Account, totals and payout history
//	POST /sellers/me/stripe/onboarding   . Stripe-hosted page for bank and identity details
//	PUT  /sellers/me/payouts/mode        . Automatic payouts, or withdraw by hand
//	POST /sellers/me/payouts/withdraw    . Pay out the available balance now
//
// Stripe (signed with Stripe-Signature, no JWT), one endpoint and one
// signing secret each:
//
//	POST /payments/stripe/webhook            . Events from the platform account
//	POST /payments/stripe/connect-webhook    . Events from connected accounts
func RegisterStripeRoutes(r chi.Router, h *handlers.StripeHandler, jwtSecret string, sellerActive func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Use(middleware.RateLimitByUser(20, time.Minute))
		r.Post("/customers/me/orders/{id}/pay", h.PayOrder)
		r.Post("/customers/me/orders/{id}/payment/sync", h.SyncPayment)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))
		r.Use(sellerActive)
		r.Get("/sellers/me/stripe", h.SellerStatus)
		r.Get("/sellers/me/payouts", h.PayoutHistory)
		r.With(middleware.RateLimitByUser(10, time.Minute)).
			Post("/sellers/me/stripe/onboarding", h.SellerConnect)
		r.Put("/sellers/me/payouts/mode", h.SetPayoutMode)
		r.With(middleware.RateLimitByUser(10, time.Minute)).
			Post("/sellers/me/payouts/withdraw", h.Withdraw)
	})

	r.Post("/payments/stripe/webhook", h.PlatformWebhook)
	r.Post("/payments/stripe/connect-webhook", h.ConnectWebhook)
}
