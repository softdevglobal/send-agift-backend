package routes

import (
	// Standard library for HTTP handling
	"net/http"

	// Chi router library - a lightweight HTTP router for Go
	"github.com/go-chi/chi/v5"
	// Chi middleware package - pre-built middleware functions
	chimw "github.com/go-chi/chi/v5/middleware"

	// Your internal handlers package - contains business logic for each route
	"myapp/internal/handlers"
)

// New builds the root HTTP router and mounts all route groups.
// This function takes all the handlers and returns the configured HTTP router.
// Parameters: handler instances for different features, and JWT secret for authentication
func New(
	// Handler instance that manages authentication logic (login, register, etc)
	auth *handlers.AuthHandler,
	// Handler instance that manages admin operations
	admin *handlers.AdminHandler,
	// Handler instance that manages country-related operations
	countries *handlers.CountryHandler,
	// Handler instance that manages customer-related operations
	customers *handlers.CustomerHandler,
	// Handler instance that manages seller-related operations
	sellers *handlers.SellerHandler,
	// Secret key used to sign and verify JWT tokens
	jwtSecret string,
	// Returns an http.Handler interface that can be used by the server
) http.Handler {
	// Create a new Chi router instance
	// This router will handle all HTTP requests
	r := chi.NewRouter()
	
	// Register middleware that runs on EVERY request
	// These are applied to all routes in order
	
	// Middleware 1: RequestID - adds a unique ID to each request
	// Useful for tracking requests through logs
	r.Use(chimw.RequestID)
	
	// Middleware 2: RealIP - extracts the real client IP address
	// Useful when behind a proxy or load balancer
	r.Use(chimw.RealIP)
	
	// Middleware 3: Logger - logs information about each request
	// Logs HTTP method, path, status code, response time, etc.
	r.Use(chimw.Logger)
	
	// Middleware 4: Recoverer - catches panics and prevents server crash
	// Returns a 500 error instead of crashing the entire application
	r.Use(chimw.Recoverer)

	// Define a GET endpoint at /health for health checks
	// Used by load balancers and monitoring to check if server is alive
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		// Set the response content type to JSON
		w.Header().Set("Content-Type", "application/json")
		// Set the HTTP status code to 200 OK
		w.WriteHeader(http.StatusOK)
		// Write the JSON response body
		// The underscores ignore the return values (number of bytes written and error)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Create a route group under /api/v1
	// All routes registered inside this function will have /api/v1 prefix
	// For example: /api/v1/login, /api/v1/users, etc.
	r.Route("/api/v1", func(r chi.Router) {
		// Register admin routes (without authentication required)
		// Example: POST /api/v1/admin/register (create first admin)
		RegisterAdminRoutes(r, auth)
		
		// Register authentication routes (login, register, refresh token, etc)
		// Example: POST /api/v1/login, POST /api/v1/register
		RegisterAuthRoutes(r, auth)
		
		// Register admin-only routes (requires valid JWT token)
		// Example: GET /api/v1/admin/dashboard, DELETE /api/v1/admin/users/:id
		RegisterAdminProtectedRoutes(r, admin, jwtSecret)
		
		// Register country-related routes (requires valid JWT token)
		// Example: GET /api/v1/countries, POST /api/v1/countries
		RegisterCountryRoutes(r, countries, jwtSecret)
		
		// Register customer-related routes (requires valid JWT token)
		// Example: GET /api/v1/customers, POST /api/v1/customers
		RegisterCustomerRoutes(r, customers, jwtSecret)
		
		// Register seller-related routes (requires valid JWT token)
		// Example: GET /api/v1/sellers, POST /api/v1/sellers
		RegisterSellerRoutes(r, sellers, jwtSecret)
	})

	// Return the fully configured router
	// This is passed to the HTTP server to handle all incoming requests
	return r
}