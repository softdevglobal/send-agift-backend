package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterCompetitionPointsRoutes mounts competition points (handover v4):
// memberships, the competition-points wallet and entries, and their admin
// controls. Every customer operation is decided on the server by the
// eligibility engine; hiding a button in a client is never the control.
//
// Customer JWT (a native app adds X-Client-Channel: ios | android):
//
//	GET  /customers/me/eligibility                       . What is available here, with reasons
//	GET  /customers/me/membership                        . Membership, paid periods, bonus state
//	GET  /customers/me/membership/offers                 . Offers on sale here, with the decision
//	GET  /customers/me/member-discount                   . Product discount on a new order now
//	POST /customers/me/membership/checkout               . Start a checkout (Idempotency-Key)
//	POST /customers/me/membership/cancel                 . Stop renewal at period end
//	POST /customers/me/membership/resume                 . Turn renewal back on
//	POST /customers/me/membership/sync                   . Confirm a checkout's payment with Stripe
//	POST /customers/me/membership/test-payment           . Sandbox only
//
// Admin JWT views and changes everything. Changes that move
// what customers are offered also need a fresh password confirmation
// (X-Reauth-Token). Activating an emergency stop deliberately does not, so
// it can happen at once.
func RegisterCompetitionPointsRoutes(r chi.Router, h *handlers.CompetitionPointsHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Get("/customers/me/eligibility", h.Eligibility)
		r.Get("/customers/me/membership", h.Membership)
		r.Get("/customers/me/membership/offers", h.Offers)
		r.Get("/customers/me/member-discount", h.MemberDiscount)

		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimitByUser(20, time.Minute))
			r.Post("/customers/me/membership/checkout", h.Join)
			r.Post("/customers/me/membership/cancel", h.CancelMembership)
			r.Post("/customers/me/membership/resume", h.ResumeMembership)
			r.Post("/customers/me/membership/sync", h.SyncMembership)
			r.Post("/customers/me/membership/test-payment", h.TestPay)
		})
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Get("/admin/competition-points/settings", h.Settings)
		r.Get("/admin/competition-points/offers", h.AdminOffers)
		r.Get("/admin/competition-points/members", h.AdminMembers)
		r.Get("/admin/competition-points/approvals", h.AdminApprovals)
		r.Get("/admin/competition-points/stops", h.AdminStops)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Post("/admin/competition-points/stops", h.ActivateStop)
		r.Post("/admin/competition-points/run", h.RunNow)

		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireReauth(jwtSecret))
			r.Post("/admin/competition-points/offers", h.CreateOffer)
			r.Put("/admin/competition-points/offers/{offerID}", h.UpdateOffer)
			r.Delete("/admin/competition-points/offers/{offerID}", h.DeleteOffer)
			r.Post("/admin/competition-points/offers/{offerID}/{action}", h.OfferAction)
			r.Post("/admin/competition-points/approvals", h.RequestApproval)
			r.Post("/admin/competition-points/approvals/{approvalID}/{action}", h.ApprovalAction)
			r.Post("/admin/competition-points/stops/{stopID}/lift", h.LiftStop)
		})
	})
}
