package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterGameRoutes mounts the skill-game collection.
//
// Same URLs for guests and logged-in customers:
//   - Customer: Authorization: Bearer <jwt> (role=customer)
//   - Guest:    X-Guest-Token: <uuid>  (no registration needed)
//
// Catalog (public):
//
//	GET  /games                              — playable game collection
//	GET  /games/{slug}                       — one game and its rules
//	GET  /games/{slug}/leaderboard           — public board (+ my_best if identified)
//
// Play (customer JWT or guest token):
//
//	POST /games/{slug}/sessions              — start a play, returns the server seed
//	POST /games/sessions/{sessionID}/submit  — submit the move log, get the real score
func RegisterGameRoutes(r chi.Router, games *handlers.GameHandler, jwtSecret string) {
	// Public catalog. The leaderboard accepts an optional identity, so it is
	// mounted with a non-required identity middleware rather than left bare.
	r.Get("/games", games.ListGames)
	r.Get("/games/{slug}", games.GetGame)

	r.Group(func(r chi.Router) {
		// false = identity is optional; anonymous callers still get the board.
		r.Use(middleware.OptionalCustomerOrGuest(jwtSecret, false))
		r.Get("/games/{slug}/leaderboard", games.GetLeaderboard)
	})

	// Playing requires an identity so a score can be attributed and a session
	// cannot be hijacked by another device.
	r.Group(func(r chi.Router) {
		r.Use(middleware.OptionalCustomerOrGuest(jwtSecret, true))

		r.Post("/games/{slug}/sessions", games.StartSession)
		r.Post("/games/sessions/{sessionID}/submit", games.SubmitScore)
	})

	// Superadmin only: who is playing, who is winning, and the anti-cheat
	// review queue. These boards carry full names and emails, and reviewing a
	// score changes a leaderboard, so ordinary admins do not get them.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("superadmin"))

		r.Get("/admin/games", games.AdminListGames)
		r.Get("/admin/games/{slug}/leaderboard", games.AdminGameLeaderboard)
		r.Get("/admin/games/{slug}/scores", games.AdminGameScores)
		r.Post("/admin/games/scores/{sessionID}/review", games.AdminReviewScore)
	})
}
