package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/database"
)

// Every provider the app offers must be able to link an account, against a
// real Postgres, since the provider list is a database check:
//
//	TEST_DATABASE_URL=... go test ./internal/repository -run SocialIdentity -v
func TestEveryProviderCanLinkAnAccount(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping social identity test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}

	tag := uuid.NewString()[:8]
	var country, customer uuid.UUID
	if err := pool.QueryRow(ctx, `insert into core.countries (iso_code, name, default_currency, default_timezone)
		values ($1, 'Linkland', 'AUD', 'Australia/Sydney') returning id`, "Y"+tag[:1]).Scan(&country); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `insert into customer.customers
		(country_id, email, password_hash, display_name, phone, phone_e164)
		values ($1, $2, 'hash', 'Link Test', '0412 000 001', '+61412000001') returning id`,
		country, "link-"+tag+"@example.test").Scan(&customer); err != nil {
		t.Fatal(err)
	}

	repo := NewCustomerRepository(pool)
	for _, provider := range []string{"google", "facebook", "apple"} {
		subject := provider + "-" + tag
		if err := repo.LinkSocialIdentity(ctx, customer, provider, subject, ""); err != nil {
			t.Fatalf("link %s: %v", provider, err)
		}
		got, err := repo.FindSocialIdentity(ctx, provider, subject)
		if err != nil || got != customer {
			t.Fatalf("find %s: %v %v", provider, got, err)
		}
	}
}
