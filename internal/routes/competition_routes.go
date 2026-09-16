package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterCompetitionRoutes mounts skill competitions.
//
// Public (optional customer JWT adds the caller's attempts, eligibility and rank):
//
//	GET  /competitions                       — published competitions
//	GET  /competitions/{id}                  — rules, prize, disclosures, winners
//	GET  /competitions/{id}/leaderboard      — live board (+ my row)
//
// Customer JWT:
//
//	POST /competitions/{id}/attempts         — start an official attempt (returns the session)
//	POST /competitions/{id}/claim            — winner accepts terms and picks a delivery address
//
// Official scores are submitted through POST /games/sessions/{sessionID}/submit.
//
// Superadmin JWT: the lifecycle from draft to winners, every action audited.
func RegisterCompetitionRoutes(r chi.Router, c *handlers.CompetitionHandler, jwtSecret string) {
	r.Group(func(r chi.Router) {
		r.Use(middleware.OptionalCustomerOrGuest(jwtSecret, false))
		r.Get("/competitions", c.List)
		r.Get("/competitions/{id}", c.Get)
		r.Get("/competitions/{id}/leaderboard", c.Leaderboard)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Post("/competitions/{id}/attempts", c.StartAttempt)
		r.Post("/competitions/{id}/claim", c.ClaimPrize)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		// Superadmin only: prizes, winners and money-shaped actions.
		r.Use(middleware.RequireRole("superadmin"))

		r.Get("/admin/competitions", c.AdminList)
		r.Post("/admin/competitions", c.Create)
		r.Get("/admin/competitions/{id}", c.AdminGet)
		r.Put("/admin/competitions/{id}", c.Update)

		r.Put("/admin/competitions/{id}/prize-reserve", c.SetReserve)
		r.Post("/admin/competitions/{id}/prize-reserve/fund", c.FundReserve)
		r.Post("/admin/competitions/{id}/schedule", c.Schedule())
		r.Post("/admin/competitions/{id}/cancel", c.Cancel)
		r.Post("/admin/competitions/{id}/freeze", c.Freeze())
		r.Post("/admin/competitions/{id}/finalise", c.Finalise)

		r.Get("/admin/competitions/{id}/leaderboard", c.AdminLeaderboard)
		r.Get("/admin/competitions/{id}/review-queue", c.ReviewQueue)
		r.Post("/admin/competitions/{id}/submissions/{submissionID}/review", c.Review)

		r.Get("/admin/competitions/{id}/winners", c.Winners)
		r.Post("/admin/competitions/{id}/winners/{winnerID}/validate", c.ValidateWinner)
		r.Post("/admin/competitions/{id}/winners/{winnerID}/disqualify", c.DisqualifyWinner)
		r.Post("/admin/competitions/{id}/winners/{winnerID}/unclaimed", c.MarkUnclaimed)
		r.Post("/admin/competitions/{id}/claims/{claimID}/verify", c.AdvanceClaim("verified"))
		r.Post("/admin/competitions/{id}/claims/{claimID}/fulfil", c.AdvanceClaim("fulfilled"))
	})
}
