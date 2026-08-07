package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"

	"myapp/internal/database"
	"myapp/internal/handlers"
	"myapp/internal/repository"
	"myapp/internal/routes"
)

func main() {
	// Load environment variables from .env (ignored if the file is missing)
	_ = godotenv.Load()

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}

	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}

	// Open a connection pool to Postgres using DB_* values from the environment
	pool, err := database.Connect()
	if err != nil {
		log.Fatalf("database error: %v", err)
	}
	defer pool.Close()

	// Apply pending SQL migrations so every developer shares the same schema via Git
	migCtx, migCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer migCancel()
	if err := database.MigrateUp(migCtx, pool); err != nil {
		log.Fatalf("migration error: %v", err)
	}

	admins := repository.NewAdminRepository(pool)
	authHandler := handlers.NewAuthHandler(admins, jwtSecret)
	adminHandler := handlers.NewAdminHandler()

	router := routes.New(authHandler, adminHandler, jwtSecret)

	addr := ":" + port
	fmt.Printf("✅ Database connected: %s\n", os.Getenv("DB_NAME"))
	fmt.Printf("🚀 Server listening on http://localhost%s\n", addr)

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
