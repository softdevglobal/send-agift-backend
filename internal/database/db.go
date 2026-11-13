package database

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	// THIRD PARTY LIBRARIES for the Postgres database
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/config" // config is used to load and manage the configuration of the application
)

// defaultMaxConns is the request pool's size unless DB_MAX_CONNS says
// otherwise. pgx's own default is one per CPU, which on a small machine is
// too few once background jobs share the pool.
const defaultMaxConns = 25

// Connect builds the pool requests and jobs run their queries on, and
// verifies it with a ping.
func Connect(cfg *config.Config) (*pgxpool.Pool, error) {
	maxConns := int32(defaultMaxConns)
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("DB_MAX_CONNS"))); err == nil && v > 0 {
		maxConns = int32(v)
	}
	return connect(cfg, maxConns, 2)
}

// ConnectLocks builds a small pool used only to hold the background jobs'
// advisory locks (see Exclusive). A job keeps its lock's connection for as
// long as it runs and does its work on the main pool, so lock holders can
// never take the connections requests and the jobs themselves need. With
// both on one pool, ten jobs starting at once held all ten connections and
// waited forever for an eleventh: every request then hung.
func ConnectLocks(cfg *config.Config, jobs int) (*pgxpool.Pool, error) {
	return connect(cfg, int32(jobs+2), 0)
}

func connect(cfg *config.Config, maxConns, minConns int32) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("unable to read database settings: %w", err)
	}
	poolCfg.MaxConns = maxConns
	poolCfg.MinConns = minConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to reach database: %w", err)
	}
	return pool, nil
}
