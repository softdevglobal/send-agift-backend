package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterCompetitionRoutes mounts skill competitions and the progressive
// prize engine that runs every one of them.
//
// Public (optional customer JWT adds the caller's plays, eligibility, points
// and rank):
//
//	GET  /competitions                      . Published rounds with their live prize
//	GET  /competitions/{id}                 . Rules, prize, disclosures, winners
//	GET  /competitions/{id}/leaderboard     . Live board (+ my row)
//	GET  /competitions/{id}/events          . Live prize stream (Server-Sent Events)
//
// Customer JWT:
//
//	POST /competitions/{id}/plays           . One idempotent play (Idempotency-Key header)
//	POST /competitions/{id}/attempts        . The same, for older app builds
//	POST /competitions/{id}/claim           . Winner accepts terms and picks a delivery address
//
// Official scores are submitted through POST /games/sessions/{sessionID}/submit.
//
// Admin JWT (support): lists, ledger, plays, analytics, boards, the score
// review queue and the leaderboard freeze, plus creating, duplicating and
// editing drafts.
//
// Superadmin JWT: everything that changes prize economics or pays out.
// editing a published competition, reserve, publishing, pause/resume/close/cancel, prize adjustments,
// voids and refunds, finalising, draws, winners, claims, settlement and
// points (spec §2, AC-13). Every one is audited, and the ones that move money
// also need a fresh password confirmation (X-Reauth-Token).
func RegisterCompetitionRoutes(r chi.Router, c *handlers.CompetitionHandler, p *handlers.PointsHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.OptionalCustomerOrGuest(jwtSecret, false))
		r.Get("/competitions", c.List)
		r.Get("/competitions/{id}", c.Get)
		r.Get("/competitions/{id}/leaderboard", c.Leaderboard)
		r.Get("/competitions/{id}/events", c.Events)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Get("/customers/me/points", p.MyWallet)
		r.Get("/customers/me/points/earning", p.MyEarningRule)
		r.Post("/competitions/{id}/claim", c.ClaimPrize)

		r.Group(func(r chi.Router) {
			// A person tapping Play cannot manage these; a script can. Per
			// account, per device, and. Set high, so a shared network is
			// not blocked for one account. Per address (spec §7).
			r.Use(middleware.RateLimitByUser(20, time.Minute))
			r.Use(middleware.RateLimitByDevice(20, time.Minute))
			r.Use(middleware.RateLimitPlaysByIP(120, time.Minute))
			r.Post("/competitions/{id}/plays", c.Play)
			r.Post("/competitions/{id}/attempts", c.StartAttempt)
		})
	})

	// Support: view only.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))

		r.Get("/admin/competitions", c.AdminList)
		r.Get("/admin/competitions/{id}", c.AdminGet)
		// Any admin may set up drafts: nothing moves money and no player
		// sees one until a Super Admin funds and publishes it. Editing a
		// published competition is Super Admin only, checked in the service.
		r.Post("/admin/competitions", c.Create)
		r.Put("/admin/competitions/{id}", c.Update)
		r.Patch("/admin/competitions/{id}", c.Update)
		r.Post("/admin/competitions/{id}/duplicate", c.Duplicate)
		r.Get("/admin/competitions/{id}/ledger", c.Ledger)
		r.Get("/admin/competitions/{id}/plays", c.Plays)
		r.Get("/admin/competitions/{id}/analytics", c.Analytics)
		r.Post("/admin/competitions/{id}/reconcile", c.Reconcile)
		r.Get("/admin/competitions/{id}/leaderboard", c.AdminLeaderboard)
		r.Get("/admin/competitions/{id}/review-queue", c.ReviewQueue)
		r.Post("/admin/competitions/{id}/submissions/{submissionID}/review", c.Review)
		r.Post("/admin/competitions/{id}/freeze", c.Freeze())
		r.Get("/admin/competitions/{id}/winners", c.Winners)
		r.Get("/admin/competitions/{id}/draw", c.Draw)
		r.Get("/admin/points/earning-rules", p.EarningRules)
		r.Get("/admin/customers", p.SearchCustomers)
		r.Get("/admin/customers/{customerID}/points", p.CustomerWallet)
	})

	// Super Admin: prize economics and payouts.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("superadmin"))

		r.Put("/admin/competitions/{id}/prize-reserve", c.SetReserve)
		r.Post("/admin/competitions/{id}/prize-reserve/fund", c.FundReserve)
		r.Post("/admin/competitions/{id}/schedule", c.Schedule())
		r.Post("/admin/competitions/{id}/activate", c.Schedule())
		r.Post("/admin/competitions/{id}/pause", c.Pause())
		r.Post("/admin/competitions/{id}/resume", c.Resume())
		r.Post("/admin/competitions/{id}/close", c.Close())

		r.Post("/admin/competitions/{id}/finalise", c.Finalise)
		r.Post("/admin/competitions/{id}/winners/{winnerID}/validate", c.ValidateWinner)
		r.Post("/admin/competitions/{id}/winners/{winnerID}/disqualify", c.DisqualifyWinner)
		r.Post("/admin/competitions/{id}/winners/{winnerID}/unclaimed", c.MarkUnclaimed)
		r.Post("/admin/competitions/{id}/claims/{claimID}/verify", c.AdvanceClaim("verified"))
		r.Post("/admin/competitions/{id}/claims/{claimID}/fulfil", c.AdvanceClaim("fulfilled"))

		r.Post("/admin/points/earning-runs", p.RunEarning)

		// Anything that moves money or points, or picks winners by chance,
		// also needs the admin's password confirmed in the last few minutes
		// (POST /admin/reauth).
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireReauth(jwtSecret))
			r.Post("/admin/competitions/{id}/cancel", c.Cancel)
			r.Post("/admin/competitions/{id}/prize-adjustments", c.AdjustPrize)
			r.Post("/admin/competitions/{id}/plays/{playID}/void", c.VoidPlay)
			r.Post("/admin/competitions/{id}/settle", c.Settle)
			r.Post("/admin/competitions/{id}/draw", c.RunDraw())
			r.Post("/admin/customers/{customerID}/points/adjustments", p.Adjust)
			r.Put("/admin/points/earning-rules/{countryID}", p.SetEarningRule)
		})
	})
}
