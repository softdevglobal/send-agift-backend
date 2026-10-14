package routes

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterFreeGiftRoutes mounts the first free gift program. Every route
// answers 404 FREE_GIFT_DISABLED while FREE_GIFT_ENABLED is off.
//
// Public:
//
//	GET /free-gifts/products?country=&latitude=&longitude=&delivery_date=
//	                                                  . Products that can be sent as a free gift now,
//	                                                    optionally only those deliverable to a place and day
//
// Customer JWT:
//
//	GET  /customers/me/free-gift?recipient_id=        . Still available, what is missing, can this recipient receive
//	POST /customers/me/free-gifts                     . Send it. Creates the order (Idempotency-Key)
//
// Seller JWT:
//
//	GET    /sellers/me/free-gift-program              . Budget cap, held, spent, remaining, status
//	PUT    /sellers/me/free-gift-program              . Turn on or pause (the budget is set by an admin)
//	GET    /sellers/me/free-gift-products             . Products in the program
//	PUT    /sellers/me/free-gift-products/{productID} . Add a product, change its cap, pause it
//	DELETE /sellers/me/free-gift-products/{productID} . Take a product out
//
// Admin JWT:
//
//	GET   /admin/free-gifts                           . Every seller's program
//	GET   /admin/free-gifts/orders                    . Every free gift sent: seller, customer, recipient, product, cost
//	PUT   /admin/free-gifts/sellers/{sellerID}/program . Set a seller's marketing budget and start or pause it
//	GET   /admin/free-gifts/countries                 . Budgets set by country
//	PUT   /admin/free-gifts/countries/{countryID}     . One budget for every seller in a country
//	PATCH /admin/free-gifts/sellers/{sellerID}        . Stop or re-allow one seller's program
func RegisterFreeGiftRoutes(r chi.Router, h *handlers.FreeGiftHandler, jwtSecret string, sellerActive func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(h.RequireEnabled)

		r.With(middleware.RateLimitByIP(60, time.Minute)).Get("/free-gifts/products", h.ListProducts)

		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireAuth(jwtSecret))
			r.Use(middleware.RequireRole("customer"))
			r.Get("/customers/me/free-gift", h.Status)
			r.With(middleware.RateLimitByUser(10, time.Minute)).Post("/customers/me/free-gifts", h.Send)
		})

		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireAuth(jwtSecret))
			r.Use(middleware.RequireRole("seller"))
			r.Use(sellerActive)
			r.Get("/sellers/me/free-gift-program", h.Program)
			r.Put("/sellers/me/free-gift-program", h.SetProgram)
			r.Get("/sellers/me/free-gift-products", h.SellerProducts)
			r.Put("/sellers/me/free-gift-products/{productID}", h.SetProduct)
			r.Delete("/sellers/me/free-gift-products/{productID}", h.RemoveProduct)
		})

		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireAuth(jwtSecret))
			r.Use(middleware.RequireRole("admin"))
			r.Get("/admin/free-gifts", h.AdminList)
			r.Get("/admin/free-gifts/orders", h.AdminOrders)
			r.Put("/admin/free-gifts/sellers/{sellerID}/program", h.AdminSetProgram)
			r.Get("/admin/free-gifts/countries", h.AdminCountryBudgets)
			r.Put("/admin/free-gifts/countries/{countryID}", h.AdminSetCountryBudget)
			r.Patch("/admin/free-gifts/sellers/{sellerID}", h.AdminSetSeller)
		})
	})
}
