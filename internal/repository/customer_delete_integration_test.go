package repository

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/database"
)

// Account deletion runs against a real Postgres, since what matters is which
// rows survive:
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
//	    go test ./internal/repository -run DeleteAccount -v
func TestDeleteAccount(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping account deletion test")
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

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scan := func(dest any, sql string, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	tag := uuid.NewString()[:8]
	var country, customer, recipient, order uuid.UUID
	scan(&country, `insert into core.countries (iso_code, name, default_currency, default_timezone)
		values ($1, 'Testland', 'AUD', 'Australia/Sydney') returning id`, "Z"+tag[:1])
	scan(&customer, `insert into customer.customers
		(country_id, email, password_hash, display_name, phone, phone_e164)
		values ($1, $2, 'hash', 'Jane Sender', '0412 000 000', '+61412000000') returning id`,
		country, "jane-"+tag+"@example.test")
	exec(`insert into customer.customer_addresses (customer_id, country_id, line1, city)
		values ($1, $2, '1 Sender St', 'Sydney')`, customer, country)
	scan(&recipient, `insert into customer.recipients (customer_id, name, email, phone)
		values ($1, 'Rita Recipient', 'rita@example.test', '+61400000000') returning id`, customer)
	exec(`insert into customer.recipient_addresses (recipient_id, country_id, line1, city, is_default)
		values ($1, $2, '9 Recipient Rd', 'Sydney', true)`, recipient, country)
	scan(&order, `insert into marketplace.orders
		(order_number, customer_id, recipient_id, country_id, delivery_date, status, currency)
		values ($1, $2, $3, $4, current_date, 'dispatched', 'AUD') returning id`,
		"T-"+tag, customer, recipient, country)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `delete from marketplace.orders where id = $1`, order)
		_, _ = pool.Exec(c, `delete from customer.customers where id = $1`, customer)
		_, _ = pool.Exec(c, `delete from core.countries where id = $1`, country)
	})

	repo := NewCustomerRepository(pool)

	// A gift on its way: refused, and nothing changes.
	if err := repo.DeleteAccount(ctx, customer.String()); !errors.Is(err, ErrCustomerActiveOrders) {
		t.Fatalf("with a dispatched order: got %v, want ErrCustomerActiveOrders", err)
	}
	var email string
	scan(&email, `select email from customer.customers where id = $1`, customer)
	if email != "jane-"+tag+"@example.test" {
		t.Fatalf("refused deletion still changed the email to %q", email)
	}

	// Delivered: the account goes, the order stays.
	exec(`update marketplace.orders set status = 'delivered' where id = $1`, order)
	if err := repo.DeleteAccount(ctx, customer.String()); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var (
		status                   string
		phone, e164, name        *string
		deletedAt, sessionsAfter *time.Time
		addresses, recipients    int
		orderCustomer            uuid.UUID
		orderRecipient           *uuid.UUID
	)
	if err := pool.QueryRow(ctx, `
		select email, status, phone, phone_e164, display_name, deleted_at, sessions_valid_after
		from customer.customers where id = $1`, customer).
		Scan(&email, &status, &phone, &e164, &name, &deletedAt, &sessionsAfter); err != nil {
		t.Fatal(err)
	}
	if email != "deleted-"+customer.String()+"@deleted.invalid" || status != "deleted" ||
		phone != nil || e164 != nil || name != nil || deletedAt == nil || sessionsAfter == nil {
		t.Fatalf("customer not anonymized: email=%q status=%q phone=%v e164=%v name=%v",
			email, status, phone, e164, name)
	}
	scan(&addresses, `select count(*) from customer.customer_addresses where customer_id = $1`, customer)
	scan(&recipients, `select count(*) from customer.recipients where customer_id = $1`, customer)
	if addresses != 0 || recipients != 0 {
		t.Fatalf("personal data left: %d addresses, %d recipients", addresses, recipients)
	}
	if err := pool.QueryRow(ctx, `select customer_id, recipient_id from marketplace.orders where id = $1`, order).
		Scan(&orderCustomer, &orderRecipient); err != nil {
		t.Fatalf("order was not kept: %v", err)
	}
	if orderCustomer != customer || orderRecipient != nil {
		t.Fatalf("order should keep its customer id and lose the recipient link: %v %v", orderCustomer, orderRecipient)
	}

	// Signing in by the old email finds nobody; deleting again is not found.
	if _, err := repo.GetByEmail(ctx, "jane-"+tag+"@example.test"); !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("old email still signs in: %v", err)
	}
	if err := repo.DeleteAccount(ctx, customer.String()); !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("second delete: got %v, want ErrCustomerNotFound", err)
	}
}
