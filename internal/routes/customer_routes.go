package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterCustomerRoutes mounts customer auth, profile, address, and saved-gift routes.
func RegisterCustomerRoutes(r chi.Router, customers *handlers.CustomerHandler, orders *handlers.OrderHandler, emailChange *handlers.EmailChangeHandler, jwtSecret string) {
	r.Post("/customers/register", customers.Register) // register a new customer
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Get("/admin/customers/{id}", customers.AdminGet)
	})
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(20, time.Minute))
		r.Post("/customers/forgot-password", customers.ForgotPassword)
		r.Post("/customers/reset-password", customers.ResetPassword)
		// "This wasn't me" link from the old inbox. A POST so a mail scanner
		// opening the link cannot trigger it.
		r.Post("/customers/email/undo", emailChange.Undo)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))                  // require authentication
		r.Use(middleware.RequireRole("customer"))                 // require role
		r.Get("/customers/me", customers.Me)                      // get the customer's profile
		r.Put("/customers/me", customers.UpdateMe)                // update the customer's profile
		r.Delete("/customers/me", customers.DeleteMe)             // delete the customer's profile
		r.Put("/customers/me/password", customers.ChangePassword) // change the customer's password
		r.Post("/customers/me/password/code", customers.SendPasswordCode)
		r.Post("/customers/me/password/reset", customers.ResetProfilePassword)
		r.With(middleware.RateLimitByIP(5, time.Minute)).Post("/customers/me/email/change", emailChange.Request)
		r.With(middleware.RateLimitByIP(10, time.Minute)).Post("/customers/me/email/confirm", emailChange.Confirm)
		r.Post("/customers/me/addresses", customers.AddAddress)           // add a new address to the customer's profile
		r.Delete("/customers/me/addresses/{id}", customers.DeleteAddress) // delete an address from the customer's profile

		// Saved gifts (wishlist). No PUT; change = delete + create
		r.Get("/customers/me/saved-gifts", customers.ListSavedGifts)
		r.Post("/customers/me/saved-gifts", customers.AddSavedGift)
		r.Delete("/customers/me/saved-gifts/{id}", customers.DeleteSavedGift)

		r.Post("/customers/me/recipients", customers.CreateRecipient)
		r.Get("/customers/me/recipients", customers.ListRecipients)
		r.Get("/customers/me/recipients/{id}", customers.GetRecipient)
		r.Put("/customers/me/recipients/{id}", customers.UpdateRecipient)
		r.Delete("/customers/me/recipients/{id}", customers.DeleteRecipient)
		r.Post("/customers/me/recipients/{id}/addresses", customers.AddRecipientAddress)
		r.Put("/customers/me/recipients/{id}/addresses/{addressId}", customers.UpdateRecipientAddress)
		r.Delete("/customers/me/recipients/{id}/addresses/{addressId}", customers.DeleteRecipientAddress)

		r.Post("/customers/me/orders", orders.Create)
		r.Get("/customers/me/orders", orders.List)
		r.Get("/customers/me/orders/{id}", orders.Get)
		r.Post("/customers/me/orders/{id}/cancel", orders.Cancel)

		// Delivered gifts sent to this customer, which they can review
		r.Get("/customers/me/received-gifts", orders.ReceivedGifts)
	})
}
