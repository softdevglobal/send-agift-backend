package routes

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"myapp/internal/handlers"
)

// New builds the root HTTP router and mounts all route groups.
func New(
	auth *handlers.AuthHandler,
	admin *handlers.AdminHandler,
	countries *handlers.CountryHandler,
	customers *handlers.CustomerHandler,
	sellers *handlers.SellerHandler,
	jwtSecret string,
) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	r.Route("/api/v1", func(r chi.Router) {
		RegisterAdminRoutes(r, auth)
		RegisterAuthRoutes(r, auth)
		RegisterAdminProtectedRoutes(r, admin, jwtSecret)
		RegisterCountryRoutes(r, countries, jwtSecret)
		RegisterCustomerRoutes(r, customers, jwtSecret)
		RegisterSellerRoutes(r, sellers, jwtSecret)
	})

	return r
}
