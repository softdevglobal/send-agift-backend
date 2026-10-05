package routes

import (
	"net/http"

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
	// requireApproved admits only sellers an admin has approved.
	requireApproved func(http.Handler) http.Handler,
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

		// Reading is open to every seller; opening shops, listing gifts and
		// taking orders waits for an admin to approve the account.
		approved := r.With(requireApproved)

		r.Get("/sellers/me/shops", sellers.ListShops)
		approved.Post("/sellers/me/shops", sellers.CreateShop)
		approved.Put("/sellers/me/shops/{id}", sellers.UpdateShop)
		approved.Delete("/sellers/me/shops/{id}", sellers.DeleteShop)
		r.Get("/sellers/me/shops/{shopID}/delivery-zones", sellers.ListDeliveryZones)
		approved.Put("/sellers/me/shops/{shopID}/delivery-zones", sellers.ReplaceDeliveryZones)

		r.Get("/sellers/me/shops/{shopID}/products", products.ListByShop)
		approved.Post("/sellers/me/shops/{shopID}/products", products.Create)

		r.Get("/sellers/me/products/{id}", products.Get)
		approved.Put("/sellers/me/products/{id}", products.Update)
		approved.Delete("/sellers/me/products/{id}", products.Delete)

		r.Get("/sellers/me/products/{id}/inventory", products.GetInventory)
		approved.Put("/sellers/me/products/{id}/inventory", products.UpdateInventory)

		r.Get("/sellers/me/order-items", sellerOrders.ListItems)
		r.Get("/sellers/me/order-items/{id}", sellerOrders.GetItem)
		approved.Patch("/sellers/me/order-items/{id}/accept", sellerOrders.AcceptItem)
	})
}
