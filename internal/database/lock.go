package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Advisory lock keys. Each names one piece of work that must only ever run on
// one API server at a time, however many are running. Postgres holds the
// lock for the session that took it and drops it if that server dies, so a
// crash never leaves it stuck.
const (
	LockMigrations          int64 = 7_301_001
	LockPointsEarning       int64 = 7_301_002
	LockPrizeReconciliation int64 = 7_301_003
	LockPushDelivery        int64 = 7_301_004
)

// WithLock runs fn while holding the advisory lock key, first waiting for any
// other server holding it to finish.
func WithLock(ctx context.Context, pool *pgxpool.Pool, key int64, fn func(context.Context) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for lock %d: %w", key, err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
		return fmt.Errorf("take lock %d: %w", key, err)
	}
	// The same connection must release it; a fresh context, so a cancelled
	// ctx cannot leave the lock held on a pooled connection.
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key) //nolint:errcheck

	return fn(ctx)
}

// TryWithLock runs fn only if no other server holds the advisory lock key,
// and reports whether it ran. It never waits: a scheduled job whose turn
// another server already took simply skips this round.
func TryWithLock(ctx context.Context, pool *pgxpool.Pool, key int64, fn func(context.Context) error) (bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire connection for lock %d: %w", key, err)
	}
	defer conn.Release()

	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&got); err != nil {
		return false, fmt.Errorf("try lock %d: %w", key, err)
	}
	if !got {
		return false, nil
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key) //nolint:errcheck

	return true, fn(ctx)
}

// Exclusive binds TryWithLock to one pool and key, for a scheduled job that
// must run on only one server each round.
func Exclusive(pool *pgxpool.Pool, key int64) func(context.Context, func(context.Context) error) (bool, error) {
	return func(ctx context.Context, fn func(context.Context) error) (bool, error) {
		return TryWithLock(ctx, pool, key, fn)
	}
}
