package routes

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterGameEntriesRoutes wires customers' game entries and state,
// sellers' advertising packages, and the admin controls for both.
func RegisterGameEntriesRoutes(r chi.Router, h *handlers.GameEntriesHandler, jwtSecret string, sellerActive func(http.Handler) http.Handler) {
	// The states of a country, for sign-up forms.
	r.Get("/countries/{countryID}/states", h.States)

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("customer"))
		r.Get("/customers/me/game-entries", h.Wallet)
		r.Get("/customers/me/state", h.MyState)

		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimitByUser(60, time.Minute))
			r.Post("/customers/me/game-entries/offers", h.Offers)
		})
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimitByUser(10, time.Minute))
			r.Post("/customers/me/game-entries/free", h.ClaimFree)
			r.Put("/customers/me/state", h.SetMyState)
		})
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))
		r.Use(sellerActive)
		r.Get("/sellers/me/advertising", h.Advertising)
		r.Post("/sellers/me/advertising/reminders/read", h.ReadReminders)
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimitByUser(30, time.Minute))
			r.Post("/sellers/me/advertising/purchases", h.Buy)
			r.Post("/sellers/me/advertising/purchases/{purchaseID}/cancel", h.CancelPurchase())
			r.Post("/sellers/me/advertising/purchases/{purchaseID}/sync", h.SyncPurchase())
			r.Post("/sellers/me/advertising/purchases/{purchaseID}/test-payment", h.TestPay())
			r.Put("/sellers/me/advertising/purchases/{purchaseID}/promotions", h.SetPromotions)
		})
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Get("/admin/advertising/packages", h.AdminPackages)
		r.Get("/admin/advertising/purchases", h.AdminPurchases)
		r.Get("/admin/games-control", h.Control)
		r.Get("/admin/countries/{countryID}/states", h.AdminStates)
		r.Get("/admin/customers", h.SearchCustomers)
		r.Get("/admin/customers/{customerID}/game-entries", h.CustomerWallet)
		r.Get("/admin/game-entries/shortfalls", h.Shortfalls)

		// Emergency stops are one step: no password re-entry in the way.
		r.Post("/admin/games-control/program/stop", h.StopProgram)
		r.Post("/admin/games-control/jurisdictions/{jurisdictionID}/stop", h.StopJurisdiction())
		r.Post("/admin/advertising/purchases/{purchaseID}/reminder", h.ResendReminder)

		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireReauth(jwtSecret))
			r.Post("/admin/advertising/packages", h.CreatePackage)
			r.Put("/admin/advertising/packages/{packageID}", h.UpdatePackage)
			r.Delete("/admin/advertising/packages/{packageID}", h.DeletePackage)
			r.Post("/admin/advertising/packages/{packageID}/{action}", h.PackageAction)
			r.Post("/admin/advertising/purchases/{purchaseID}/confirm", h.ConfirmPurchase)
			r.Put("/admin/games-control/program", h.SetProgram)
			r.Put("/admin/games-control/jurisdictions", h.SaveJurisdiction)
			r.Post("/admin/games-control/jurisdictions/{jurisdictionID}/lift", h.LiftJurisdiction())
			r.Post("/admin/states", h.CreateState)
			r.Put("/admin/states/{stateID}", h.UpdateState)
			r.Delete("/admin/states/{stateID}", h.DeleteState)
			r.Post("/admin/customers/{customerID}/game-entries/adjustments", h.Adjust)
			r.Post("/admin/game-entries/shortfalls/{caseID}/resolve", h.ResolveShortfall)
		})
	})
}
