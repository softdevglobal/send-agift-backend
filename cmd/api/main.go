package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"myapp/internal/config"
	"myapp/internal/database"
	"myapp/internal/handlers"
	"myapp/internal/repository"
	"myapp/internal/routes"
	"myapp/internal/services"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	pool, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("database error: %v", err)
	}
	defer pool.Close()

	migCtx, migCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer migCancel()
	if err := database.MigrateUp(migCtx, pool); err != nil {
		log.Fatalf("migration error: %v", err)
	}

	admins := repository.NewAdminRepository(pool)
	countries := repository.NewCountryRepository(pool)
	customers := repository.NewCustomerRepository(pool)
	sellers := repository.NewSellerRepository(pool)

	authService := services.NewAuthService(admins, cfg.JWTSecret, cfg.BootstrapSecret, cfg.JWTExpiry)
	adminService := services.NewAdminService(admins)
	countryService := services.NewCountryService(countries)
	customerService := services.NewCustomerService(customers, countries, cfg.JWTSecret, cfg.JWTExpiry)
	sellerService := services.NewSellerService(sellers, countries, cfg.JWTSecret, cfg.JWTExpiry)

	authHandler := handlers.NewAuthHandler(authService)
	adminHandler := handlers.NewAdminHandler(adminService)
	countryHandler := handlers.NewCountryHandler(countryService)
	customerHandler := handlers.NewCustomerHandler(customerService)
	sellerHandler := handlers.NewSellerHandler(sellerService)

	router := routes.New(authHandler, adminHandler, countryHandler, customerHandler, sellerHandler, cfg.JWTSecret)

	addr := ":" + cfg.AppPort
	fmt.Printf("✅ Database connected: %s\n", cfg.DBName)
	fmt.Printf("🚀 Server listening on http://localhost%s\n", addr)

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
