package routes

import (
	"github.com/go-chi/chi/v5"

	"myapp/internal/handlers"
	"myapp/internal/middleware"
)

// RegisterSellerRoutes mounts seller auth, profile, address, shop, product, and inventory routes.
func RegisterSellerRoutes(
	r chi.Router,
	sellers *handlers.SellerHandler,
	products *handlers.ProductHandler,
	jwtSecret string,
) {
	r.Post("/sellers/register", sellers.Register)

	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(jwtSecret))
		r.Use(middleware.RequireRole("seller"))
		r.Get("/sellers/me", sellers.Me)
		r.Put("/sellers/me", sellers.UpdateMe)
		r.Delete("/sellers/me", sellers.DeleteMe)

		r.Post("/sellers/me/addresses", sellers.AddAddress)
		r.Put("/sellers/me/addresses/{id}", sellers.UpdateAddress)
		r.Delete("/sellers/me/addresses/{id}", sellers.DeleteAddress)

		r.Post("/sellers/me/shops", sellers.CreateShop)
		r.Put("/sellers/me/shops/{id}", sellers.UpdateShop)
		r.Delete("/sellers/me/shops/{id}", sellers.DeleteShop)

		r.Get("/sellers/me/shops/{shopID}/products", products.ListByShop)
		r.Post("/sellers/me/shops/{shopID}/products", products.Create)

		r.Get("/sellers/me/products/{id}", products.Get)
		r.Put("/sellers/me/products/{id}", products.Update)
		r.Delete("/sellers/me/products/{id}", products.Delete)

		r.Get("/sellers/me/products/{id}/inventory", products.GetInventory)
		r.Put("/sellers/me/products/{id}/inventory", products.UpdateInventory)
	})
}
