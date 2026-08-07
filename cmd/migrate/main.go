package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"

	"myapp/internal/database"
)

func main() {
	_ = godotenv.Load()

	pool, err := database.Connect()
	if err != nil {
		log.Fatalf("database error: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := database.MigrateUp(ctx, pool); err != nil {
		log.Fatalf("migrate failed: %v", err)
	}

	log.Println("migrations complete")
	os.Exit(0)
}
