package routes

import (
	"time"

	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterSellerRoutes mounts seller auth, profile, address, shop, product, and inventory routes.
func RegisterSellerRoutes(
	r chi.Router,
	sellers *handlers.SellerHandler,
	sellerOrders *handlers.SellerOrderHandler,
	products *handlers.ProductHandler,
	jwtSecret string,
) {
	r.Post("/sellers/register", sellers.Register)
	r.Get("/sellers/shops/slug-available", sellers.ShopSlugAvailable)
	r.Group(func(r chi.Router) {
		r.Use(middleware.RateLimitByIP(20, time.Minute))
		r.Post("/sellers/verify-email", sellers.VerifyEmail)
		r.Post("/sellers/verify-email/resend", sellers.ResendEmailCode)
		r.Post("/sellers/forgot-password", sellers.ForgotPassword)
		r.Post("/sellers/reset-password", sellers.ResetPassword)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("admin"))
		r.Get("/admin/sellers", sellers.AdminList)
		r.Get("/admin/sellers/{id}", sellers.AdminGet)
		r.Patch("/admin/sellers/{id}/status", sellers.AdminSetStatus)
		r.Patch("/admin/sellers/{id}/verification", sellers.AdminSetVerification)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))
		r.Use(sellers.RequireActive)
		r.Get("/sellers/me", sellers.Me)
		r.Post("/sellers/me/password/code", sellers.SendPasswordCode)
		r.Post("/sellers/me/password/reset", sellers.ResetProfilePassword)
		r.Put("/sellers/me", sellers.UpdateMe)
		r.Delete("/sellers/me", sellers.DeleteMe)

		r.Post("/sellers/me/addresses", sellers.AddAddress)
		r.Put("/sellers/me/addresses/{id}", sellers.UpdateAddress)
		r.Delete("/sellers/me/addresses/{id}", sellers.DeleteAddress)

		r.Get("/sellers/me/shops", sellers.ListShops)
		r.Post("/sellers/me/shops", sellers.CreateShop)
		r.Put("/sellers/me/shops/{id}", sellers.UpdateShop)
		r.Delete("/sellers/me/shops/{id}", sellers.DeleteShop)
		r.Get("/sellers/me/shops/{shopID}/delivery-zones", sellers.ListDeliveryZones)
		r.Put("/sellers/me/shops/{shopID}/delivery-zones", sellers.ReplaceDeliveryZones)

		r.Get("/sellers/me/shops/{shopID}/products", products.ListByShop)
		r.Post("/sellers/me/shops/{shopID}/products", products.Create)

		r.Get("/sellers/me/products/{id}", products.Get)
		r.Put("/sellers/me/products/{id}", products.Update)
		r.Delete("/sellers/me/products/{id}", products.Delete)

		r.Get("/sellers/me/products/{id}/inventory", products.GetInventory)
		r.Put("/sellers/me/products/{id}/inventory", products.UpdateInventory)

		r.Get("/sellers/me/order-items", sellerOrders.ListItems)
		r.Get("/sellers/me/order-items/{id}", sellerOrders.GetItem)
		r.Patch("/sellers/me/order-items/{id}/accept", sellerOrders.AcceptItem)
	})
}
