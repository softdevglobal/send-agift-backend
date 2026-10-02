package database

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to TEST_DATABASE_URL, or skips: these tests need a real
// Postgres, which CI provides.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping advisory lock tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestTryWithLockSkipsWhileAnotherServerHoldsIt(t *testing.T) {
	ctx := context.Background()
	// Two pools stand in for two API servers.
	first, second := testPool(t), testPool(t)
	const key int64 = 7_309_999

	inside := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := TryWithLock(ctx, first, key, func(context.Context) error {
			close(inside)
			<-release
			return nil
		})
		done <- err
	}()
	<-inside

	ran, err := TryWithLock(ctx, second, key, func(context.Context) error {
		t.Error("second server ran the job while the first held the lock")
		return nil
	})
	if err != nil || ran {
		t.Fatalf("second server: ran=%v err=%v, want skipped", ran, err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// Released: the second server gets the next round.
	ran, err = TryWithLock(ctx, second, key, func(context.Context) error { return nil })
	if err != nil || !ran {
		t.Fatalf("after release: ran=%v err=%v, want ran", ran, err)
	}
}

func TestMigrateUpFromTwoServersAtOnce(t *testing.T) {
	ctx := context.Background()
	// A database of its own, created empty as a new environment's would be:
	// other packages' tests share TEST_DATABASE_URL and run at the same time.
	// Two pools on it stand in for two API servers starting together.
	first := freshDatabase(t)
	second := connectTo(t, first.Config().ConnConfig.Database)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, pool := range []*pgxpool.Pool{first, second} {
		wg.Go(func() { errs[i] = MigrateUp(ctx, pool) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("server %d: %v", i+1, err)
		}
	}

	files, err := listMigrations(".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := first.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Fatalf("applied %d migrations, want each of the %d exactly once", applied, len(files))
	}
}

// freshDatabase creates an empty database for one test, returns a pool on
// it, and drops it afterwards.
func freshDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := testPool(t)
	name := "migrate_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := admin.Exec(context.Background(), `CREATE DATABASE `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	return connectTo(t, name)
}

// connectTo opens a pool on another database of the TEST_DATABASE_URL server.
func connectTo(t *testing.T, database string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = database
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
