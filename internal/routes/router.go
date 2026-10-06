package routes

import (
	// Standard library for HTTP handling
	"encoding/json"
	"net/http"

	// Chi router library - a lightweight HTTP router for Go
	"github.com/go-chi/chi/v5"
	// Chi CORS - allows browsers on other origins (e.g. localhost:3000) to call this API
	"github.com/go-chi/cors"
	// Chi middleware package - pre-built middleware functions
	chimw "github.com/go-chi/chi/v5/middleware"

	// Your internal handlers package - contains business logic for each route
	"myapp/internal/handlers"
)

// BuildSHA is the git commit the binary was built from, stamped in at build
// time (go build -ldflags "-X myapp/internal/routes.BuildSHA=<sha>"). Local
// builds that skip the flag report "dev".
var BuildSHA = "dev"

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
	// Handler instance that manages country capability gates
	countryCapabilities *handlers.CountryCapabilityHandler,
	// Handler instance that manages customer-related operations
	customers *handlers.CustomerHandler,
	orders *handlers.OrderHandler,
	// Handler instance that manages public marketplace browsing
	shops *handlers.ShopsHandler,
	// Handler instance that manages seller-related operations
	sellers *handlers.SellerHandler,
	sellerOrders *handlers.SellerOrderHandler,
	// Handler instance that manages seller products and inventory
	products *handlers.ProductHandler,
	// Handler instance that manages seller reels and the public reel feed
	reels *handlers.ReelHandler,
	// Handler instance that manages public reel likes and comments
	reelSocial *handlers.ReelSocialHandler,
	// Handler instance that manages AliExpress-style product reviews
	productReviews *handlers.ProductReviewHandler,
	// Handler instance that manages customer↔seller chat
	messaging *handlers.MessagingHandler,
	// Handler instance that manages the skill-game collection and scores
	games *handlers.GameHandler,
	// Handler instance that manages skill competitions, leaderboards and winners
	competitions *handlers.CompetitionHandler,
	// Handler instance that manages customers' points wallets
	points *handlers.PointsHandler,
	// Handler instance that manages sellers buying points
	sellerPoints *handlers.SellerPointsHandler,
	// Handler instance that issues presigned S3 upload/download URLs
	media *handlers.MediaHandler,
	// Handler instance that proxies Google Places address lookups
	places *handlers.PlacesHandler,
	shipping *handlers.ShippingHandler,
	// Handler instance that checks which gifts can reach a searched address
	availability *handlers.AvailabilityHandler,
	// Handler instance that registers mobile devices for push notifications
	push *handlers.PushHandler,
	// Handler instance that signs customers in with Google and Facebook
	social *handlers.SocialAuthHandler,
	// Secret key used to sign and verify JWT tokens
	jwtSecret string,
	// Returns an http.Handler interface that can be used by the server
) http.Handler {
	// Create a new Chi router instance
	// This router will handle all HTTP requests
	r := chi.NewRouter()

	// Register middleware that runs on EVERY request
	// These are applied to all routes in order

	// CORS: needed for browser frontends (React/Vite on another port).
	// Postman / curl do not use CORS. They already work without this.
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Bootstrap-Secret", "X-Guest-Token", "Idempotency-Key", "X-App-Platform", "X-Device-Id", "X-Reauth-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

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

	// GET /version reports which commit this server was built from, so a
	// deploy can be checked against the SHA it was meant to ship.
	r.Get("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": BuildSHA})
	})

	// Create a route group under /api/v1
	// All routes registered inside this function will have /api/v1 prefix
	// For example: /api/v1/login, /api/v1/users, etc.
	r.Route("/api/v1", func(r chi.Router) {
		// Public marketplace browsing (no JWT required)
		RegisterMarketplaceRoutes(r, shops)

		// Register admin routes (without authentication required)
		// Example: POST /api/v1/admin/register (create first admin)
		RegisterAdminRoutes(r, auth)

		// Register authentication routes (login, register, refresh token, etc)
		// Example: POST /api/v1/login, POST /api/v1/register
		RegisterAuthRoutes(r, auth, social, jwtSecret)

		// Register admin-only routes (requires valid JWT token)
		// Example: GET /api/v1/admin/dashboard, DELETE /api/v1/admin/users/:id
		RegisterAdminProtectedRoutes(r, admin, jwtSecret)

		// Countries (public read) + admin country/capability CRUD
		RegisterCountryRoutes(r, countries, countryCapabilities, jwtSecret)

		// Register customer-related routes (requires valid JWT token)
		// Example: GET /api/v1/customers, POST /api/v1/customers
		RegisterCustomerRoutes(r, customers, orders, jwtSecret)

		// Register seller-related routes (requires valid JWT token)
		// Example: GET /api/v1/sellers, POST /api/v1/sellers
		RegisterSellerRoutes(r, sellers, sellerOrders, products, jwtSecret)

		// Public reel feed (no JWT) + seller reel CRUD (seller JWT)
		// Example: GET /api/v1/reels, POST /api/v1/sellers/me/shops/{shopID}/reels
		RegisterReelRoutes(r, reels, jwtSecret, sellers.RequireActive)

		// Public reel likes + comments (customer JWT or X-Guest-Token on same URLs)
		RegisterReelSocialRoutes(r, reelSocial, jwtSecret)

		// Product reviews (public list/summary + customer create/vote + seller reply)
		RegisterProductReviewRoutes(r, productReviews, jwtSecret, sellers.RequireActive)

		// Customer↔seller product inquiry and order chat
		// Example: POST /api/v1/conversations, GET /api/v1/conversations/{id}/messages
		RegisterMessagingRoutes(r, messaging, jwtSecret, sellers.RequireActive)

		// Skill-game collection: catalog, seeded sessions, server-scored results
		// Example: POST /api/v1/games/2048/sessions
		RegisterGameRoutes(r, games, jwtSecret)

		// Skill competitions: live leaderboards, official attempts, winners, prize claims
		// Example: GET /api/v1/competitions/{id}/leaderboard
		RegisterCompetitionRoutes(r, competitions, points, jwtSecret)

		// Sellers buying points, and the admin/webhook that confirm payment
		// Example: POST /api/v1/sellers/me/points/purchases
		RegisterSellerPointsRoutes(r, sellerPoints, jwtSecret, sellers.RequireActive)

		// Register media routes for presigned S3 uploads (requires valid JWT token)
		// Example: POST /api/v1/media/presign-upload
		RegisterMediaRoutes(r, media, jwtSecret, sellers.RequireActive)

		// Register Google Places address lookup routes (public, rate limited)
		// Example: GET /api/v1/places/autocomplete?input=221b+baker
		RegisterPlacesRoutes(r, places)

		RegisterShippingRoutes(r, shipping, jwtSecret, sellers.RequireActive)

		// Find gifts: which published gifts a delivery zone can reach
		// Example: GET /api/v1/availability?latitude=6.9&longitude=79.8&delivery_date=2026-10-05
		RegisterAvailabilityRoutes(r, availability)

		// Push notifications: the app registers its Firebase token after sign-in
		// Example: POST /api/v1/customers/me/push-devices
		RegisterPushRoutes(r, push, jwtSecret)
	})

	// Return the fully configured router
	// This is passed to the HTTP server to handle all incoming requests
	return r
}
