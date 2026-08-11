package main

import (
	"context"
	"log"
	"os"
	"time"

	"myapp/internal/config"
	"myapp/internal/database"
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := database.MigrateUp(ctx, pool); err != nil {
		log.Fatalf("migrate failed: %v", err)
	}

	log.Println("migrations complete")
	os.Exit(0)
}
