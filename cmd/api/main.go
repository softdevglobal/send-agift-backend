package main

import (
	"context"	// context is used to manage the lifecycle of a request
	"fmt" 		// fmt is used to format and print text 
	"log"		// log is used to log messages to the console
	"net/http"	// net/http is used to create and manage HTTP servers and clients
	"time"	// time is used to measure and manage time-related operations

	"myapp/internal/config" // config is used to load and manage the configuration of the application
	"myapp/internal/database" // database is used to connect and manage the database
	"myapp/internal/handlers" // handlers is used to handle the HTTP requests and responses
	"myapp/internal/repository" // repository is used to manage the data access layer of the application
	"myapp/internal/routes"		// routes is used to manage the routing of the application
	"myapp/internal/services"  // services is used to manage the business logic of the project
)

// func = is the function main function 
func main() {
	cfg, err := config.Load() // load application configuration from the environment variables
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	// pool is the database connection pool
	pool, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("database error: %v", err)
	}
	defer pool.Close()

	// migCtx is the context for the migration operation
	migCtx, migCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer migCancel()
	if err := database.MigrateUp(migCtx, pool); err != nil {
		log.Fatalf("migration error: %v", err)
	}


	// admins, countries, customers, sellers are the repositories for the admin, country, customer, and seller entities
	admins := repository.NewAdminRepository(pool) // create a new admin repository
	countries := repository.NewCountryRepository(pool) // create a new country repository
	customers := repository.NewCustomerRepository(pool) // create a new customer repository
	sellers := repository.NewSellerRepository(pool) // create a new seller repository
	products := repository.NewProductRepository(pool)
	orders := repository.NewOrderRepository(pool)

	// s3Service issues presigned URLs so clients upload straight to the bucket
	s3Service, err := services.NewS3Service(cfg)
	if err != nil {
		log.Fatalf("s3 error: %v", err)
	}

	authService := services.NewAuthService(admins, customers, sellers, cfg.JWTSecret, cfg.BootstrapSecret, cfg.JWTExpiry) // create a new auth service
	adminService := services.NewAdminService(admins) // create a new admin service
	countryService := services.NewCountryService(countries)
	customerService := services.NewCustomerService(customers, countries, products, cfg.JWTSecret, cfg.JWTExpiry) // create a new customer service
	orderService := services.NewOrderService(orders, customers, countries)
	sellerService := services.NewSellerService(sellers, countries, cfg.JWTSecret, cfg.JWTExpiry)
	productService := services.NewProductService(products, sellers)

	authHandler := handlers.NewAuthHandler(authService) // create a new auth handler
	adminHandler := handlers.NewAdminHandler(adminService) // create a new admin handler
	countryHandler := handlers.NewCountryHandler(countryService) // create a new country handler
	customerHandler := handlers.NewCustomerHandler(customerService) // create a new customer handler
	orderHandler := handlers.NewOrderHandler(orderService)
	marketplaceService := services.NewMarketplaceService(sellers, products)
	shopsHandler := handlers.NewShopsHandler(marketplaceService)
	sellerHandler := handlers.NewSellerHandler(sellerService) // create a new seller handler
	productHandler := handlers.NewProductHandler(productService)
	mediaHandler := handlers.NewMediaHandler(s3Service) // create a new media handler

	router := routes.New(authHandler, adminHandler, countryHandler, customerHandler, orderHandler, shopsHandler, sellerHandler, productHandler, mediaHandler, cfg.JWTSecret) // create a new router

	addr := ":" + cfg.AppPort // create a new address for the server
	fmt.Printf("✅ Database connected: %s\n", cfg.DBName) // print the database name
	fmt.Printf("🚀 Server listening on http://localhost%s\n", addr) // print the server address

	if err := http.ListenAndServe(addr, router); err != nil { // start the server
		log.Fatalf("server error: %v", err)
	} // if the server fails to start, log the error
}
