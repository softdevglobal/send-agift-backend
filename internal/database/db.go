package database

import (
	"context"
	"fmt"
	"time"
	// THIRD PARTY LIBRARIES for the Postgres database
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/config" // config is used to load and manage the configuration of the application
)

// Connect builds a Postgres pool from config and verifies it with a ping.
func Connect(cfg *config.Config) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // create a new context with a timeout of 5 seconds
	defer cancel() // cancel the context if the function returns an error

	pool, err := pgxpool.New(ctx, cfg.DSN()) // create a new pool for the database
	if err != nil { // if the pool fails to create, return an error
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}

	// if the pool fails to ping, return an error
	if err := pool.Ping(ctx); err != nil {
		pool.Close() // close the pool if the ping fails
		return nil, fmt.Errorf("unable to reach database: %w", err)
	}

	return pool, nil
}
