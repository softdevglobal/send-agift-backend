package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/database"
	"myapp/internal/models"
	"myapp/internal/repository"
)

// Integration tests for the points system: sellers buying points, product
// rewards funded from them, gift points and points prizes. Run against a
// real Postgres, because what is under test — row locks, one transaction,
// unique keys, check constraints — lives in the database.
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
//	    go test ./internal/services -run Points -v
//
// Point it at a throwaway database: it migrates it and writes test rows that
// stay (the points ledger is append-only, so nothing that touched it can be
// deleted).

type pointsFixture struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	repo    *repository.PointsRepository
	orders  *OrderService
	sellers *SellerPointsService
	country uuid.UUID
}

func newPointsFixture(t *testing.T, provider string) *pointsFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed points tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	f := &pointsFixture{t: t, ctx: ctx, pool: pool, repo: repository.NewPointsRepository(pool)}
	iso := "P" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:10])
	f.scan(&f.country, `insert into core.countries (iso_code, name, default_currency, default_timezone)
		values ($1, $2, 'USD', 'UTC') returning id`, iso, "Pointsland "+iso)
	f.orders = NewOrderService(repository.NewOrderRepository(pool), repository.NewCustomerRepository(pool),
		repository.NewCountryRepository(pool), repository.NewShipmentRepository(pool))
	p, err := NewPointsPaymentProvider(provider)
	if err != nil {
		t.Fatal(err)
	}
	f.sellers = NewSellerPointsService(f.repo, p, 10, "USD", "whsec-test")
	return f
}

func (f *pointsFixture) scan(dest any, sql string, args ...any) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(dest); err != nil {
		f.t.Fatalf("scan: %v\n%s", err, sql)
	}
}

func (f *pointsFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("exec: %v\n%s", err, sql)
	}
}

func (f *pointsFixture) seller() uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	f.scan(&id, `insert into seller.sellers (country_id, legal_name, email, password_hash)
		values ($1, 'Points Seller', $2, 'x') returning id`, f.country, uuid.NewString()+"@seller.test")
	return id
}

// product makes an active shop for seller with one published product that
// rewards reward points per unit.
func (f *pointsFixture) product(seller uuid.UUID, reward int) uuid.UUID {
	f.t.Helper()
	var addr, shop, product uuid.UUID
	f.scan(&addr, `insert into seller.seller_addresses (seller_id, country_id, line1, city)
		values ($1, $2, '1 Test Road', 'Colombo') returning id`, seller, f.country)
	slug := "shop-" + uuid.NewString()[:8]
	f.scan(&shop, `insert into seller.shops (seller_id, name, slug, address_id, status)
		values ($1, $2, $2, $3, 'active') returning id`, seller, slug, addr)
	f.scan(&product, `insert into seller.products (shop_id, name, slug, price_amount, currency, status, reward_points)
		values ($1, 'Wireless Headphones', 'headphones', 1000000, 'USD', 'published', $2) returning id`,
		shop, reward)
	return product
}

func (f *pointsFixture) customer(email string) uuid.UUID {
	f.t.Helper()
	if email == "" {
		email = uuid.NewString() + "@customer.test"
	}
	var id uuid.UUID
	f.scan(&id, `insert into customer.customers (country_id, email, password_hash, display_name)
		values ($1, $2, 'x', 'Points Customer') returning id`, f.country, email)
	return id
}

func (f *pointsFixture) recipient(customer uuid.UUID, email *string) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	f.scan(&id, `insert into customer.recipients (customer_id, name, email) values ($1, 'Friend', $2) returning id`,
		customer, email)
	return id
}

// fund gives a seller points through a confirmed purchase.
func (f *pointsFixture) fund(seller uuid.UUID, points int64) {
	f.t.Helper()
	p, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{
		Points: points, IdempotencyKey: uuid.NewString()})
	if err != nil {
		f.t.Fatalf("purchase: %v", err)
	}
	if _, _, err := f.repo.CompletePurchase(f.ctx, p.ID, repository.PurchaseConfirmation{
		PaidAmountCents: p.AmountCents, ConfirmedBy: "admin"}); err != nil {
		f.t.Fatalf("complete: %v", err)
	}
}

// grant gives a customer points.
func (f *pointsFixture) grant(customer uuid.UUID, points int64) {
	f.t.Helper()
	reason := "test float"
	if _, err := f.repo.Adjust(f.ctx, repository.PointsChange{
		CustomerID: customer, EntryType: models.PointsEntryAdminGrant, Delta: points,
		IdempotencyKey: "test:" + uuid.NewString(), Reason: &reason, ActorType: "admin",
	}, models.AuditEntry{ActorType: "admin", Action: "points.adjusted", EntityType: "customer", EntityID: &customer}); err != nil {
		f.t.Fatalf("grant: %v", err)
	}
}

func (f *pointsFixture) order(customer, product uuid.UUID, qty int, recipient *uuid.UUID, giftPoints int64) (*models.OrderDetails, error) {
	in := OrderCreateInput{
		CountryID: f.country.String(), DeliveryDate: time.Now().AddDate(0, 0, 3).Format("2006-01-02"),
		Items:      []OrderItemInput{{ProductID: product.String(), Quantity: qty}},
		GiftPoints: giftPoints,
	}
	if recipient != nil {
		s := recipient.String()
		in.RecipientID = &s
	}
	return f.orders.Create(f.ctx, customer.String(), in)
}

func (f *pointsFixture) deliver(order uuid.UUID) {
	f.exec(`update marketplace.order_items set fulfilment_status = 'delivered' where order_id = $1`, order)
	f.exec(`update marketplace.orders set status = 'delivered', updated_at = now() where id = $1`, order)
}

func (f *pointsFixture) run() *models.EarningRunResult {
	f.t.Helper()
	res, err := f.repo.RunEarning(f.ctx, 500)
	if err != nil {
		f.t.Fatalf("earning run: %v", err)
	}
	return res
}

func (f *pointsFixture) customerBalance(id uuid.UUID) int64 {
	f.t.Helper()
	b, err := f.repo.Balance(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *pointsFixture) sellerWallet(id uuid.UUID) *models.SellerPointsWallet {
	f.t.Helper()
	w, err := f.repo.SellerWallet(f.ctx, id, 100)
	if err != nil {
		f.t.Fatal(err)
	}
	return w
}

// ─── Seller purchases ─────────────────────────────────────────────────────

func TestPointsPurchaseRateAndValidation(t *testing.T) {
	f := newPointsFixture(t, "manual")
	seller := f.seller()

	for _, tc := range []struct {
		in   PurchaseInput
		want int64
	}{
		{PurchaseInput{AmountCents: 1000}, 100}, // $10.00 = 100 points
		{PurchaseInput{AmountCents: 2500}, 250}, // $25.00
		{PurchaseInput{Points: 1000}, 1000},     // $100.00
		{PurchaseInput{AmountCents: 500, Points: 50}, 50},
	} {
		tc.in.IdempotencyKey = uuid.NewString()
		p, err := f.sellers.CreatePurchase(f.ctx, seller.String(), tc.in)
		if err != nil {
			t.Fatalf("%+v: %v", tc.in, err)
		}
		if p.Points != tc.want || p.Status != models.PointsPurchasePending || p.CentsPerPoint != 10 {
			t.Fatalf("%+v: got %d points, status %s", tc.in, p.Points, p.Status)
		}
	}
	for _, bad := range []PurchaseInput{
		{AmountCents: 50},              // below $1
		{AmountCents: 1005},            // not a whole point
		{AmountCents: 1000, Points: 7}, // client's points disagree with the amount
		{AmountCents: 2_000_000},       // above the limit
	} {
		bad.IdempotencyKey = uuid.NewString()
		if _, err := f.sellers.CreatePurchase(f.ctx, seller.String(), bad); !errors.Is(err, ErrInvalidPurchase) {
			t.Fatalf("%+v: want ErrInvalidPurchase, got %v", bad, err)
		}
	}
	// Nothing is credited while payments are pending.
	if w := f.sellerWallet(seller); w.Balance != 0 {
		t.Fatalf("pending purchases credited %d", w.Balance)
	}
}

func TestPointsPurchaseIdempotentAndManualConfirm(t *testing.T) {
	f := newPointsFixture(t, "manual")
	seller := f.seller()
	key := uuid.NewString()
	a, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 1000, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 1000, IdempotencyKey: key})
	if err != nil || b.ID != a.ID {
		t.Fatalf("retry made a second purchase: %v %v", b, err)
	}
	if _, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 2000, IdempotencyKey: key}); !errors.Is(err, ErrInvalidPurchase) {
		t.Fatalf("same key, other amount: want ErrInvalidPurchase, got %v", err)
	}
	// The manual provider never lets the seller confirm their own payment.
	if _, err := f.sellers.TestPayment(f.ctx, seller.String(), a.ID, true); !errors.Is(err, ErrTestPaymentsOff) {
		t.Fatalf("want ErrTestPaymentsOff, got %v", err)
	}
	admin := AdminActor{ID: uuid.New()}
	done, err := f.sellers.AdminConfirmPurchase(f.ctx, admin, a.ID, ConfirmPurchaseInput{Note: "bank transfer seen"})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != models.PointsPurchaseCompleted || *done.PointsCredited != 100 {
		t.Fatalf("confirmed purchase: %+v", done)
	}
	// Confirming again credits nothing more.
	if _, err := f.sellers.AdminConfirmPurchase(f.ctx, admin, a.ID, ConfirmPurchaseInput{}); err != nil {
		t.Fatal(err)
	}
	w := f.sellerWallet(seller)
	if w.Balance != 100 || w.LifetimePurchased != 100 || len(w.Entries) != 1 {
		t.Fatalf("wallet after confirm: %+v", w)
	}
	e := w.Entries[0]
	if e.Category != models.PointsCategoryPointsPurchase || e.Direction != "credit" ||
		e.BalanceBefore != 0 || e.BalanceAfter != 100 || e.Status != "completed" {
		t.Fatalf("ledger row: %+v", e)
	}
	// A completed purchase cannot then be failed or cancelled.
	if _, err := f.sellers.AdminFailPurchase(f.ctx, admin, a.ID, "oops"); !errors.Is(err, ErrPurchaseState) {
		t.Fatalf("fail after complete: %v", err)
	}
}

func TestPointsPurchaseConcurrentConfirmationsCreditOnce(t *testing.T) {
	f := newPointsFixture(t, "test")
	seller := f.seller()
	p, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 5000, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.sellers.TestPayment(f.ctx, seller.String(), p.ID, true)
		}()
	}
	wg.Wait()
	if w := f.sellerWallet(seller); w.Balance != 500 || len(w.Entries) != 1 {
		t.Fatalf("concurrent confirmations: balance %d, %d entries", w.Balance, len(w.Entries))
	}
}

func TestPointsPurchaseInstantProviderCreditsAtOnce(t *testing.T) {
	f := newPointsFixture(t, "instant")
	seller := f.seller()
	key := uuid.NewString()
	p, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 2500, IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != models.PointsPurchaseCompleted || *p.PointsCredited != 250 {
		t.Fatalf("instant purchase: %+v", p)
	}
	// A retried request returns the same purchase and credits nothing more.
	if _, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 2500, IdempotencyKey: key}); err != nil {
		t.Fatal(err)
	}
	if w := f.sellerWallet(seller); w.Balance != 250 || len(w.Entries) != 1 {
		t.Fatalf("wallet: balance %d, %d entries", w.Balance, len(w.Entries))
	}
}

func TestPointsPurchaseTestProviderDecline(t *testing.T) {
	f := newPointsFixture(t, "test")
	seller := f.seller()
	p, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 1000, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.sellers.TestPayment(f.ctx, seller.String(), p.ID, false)
	if err != nil || got.Status != models.PointsPurchaseFailed {
		t.Fatalf("decline: %+v %v", got, err)
	}
	if _, err := f.sellers.TestPayment(f.ctx, seller.String(), p.ID, true); !errors.Is(err, ErrPurchaseState) {
		t.Fatalf("paying a failed purchase: %v", err)
	}
	if w := f.sellerWallet(seller); w.Balance != 0 {
		t.Fatalf("failed purchase credited %d", w.Balance)
	}
	// Another seller cannot touch it.
	if _, err := f.sellers.TestPayment(f.ctx, f.seller().String(), p.ID, true); !errors.Is(err, ErrPurchaseNotFound) {
		t.Fatalf("other seller: %v", err)
	}
}

func sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte("whsec-test"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestPointsPurchaseWebhook(t *testing.T) {
	f := newPointsFixture(t, "manual")
	seller := f.seller()
	p, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 1000, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	ref := "pi_" + uuid.NewString()
	body, _ := json.Marshal(PaymentEvent{PurchaseID: p.ID, Status: "succeeded", AmountCents: 1000, Currency: "USD", ProviderReference: &ref})
	if _, err := f.sellers.HandleWebhook(f.ctx, body, "deadbeef"); !errors.Is(err, ErrWebhookSignature) {
		t.Fatalf("bad signature: %v", err)
	}
	wrong, _ := json.Marshal(PaymentEvent{PurchaseID: p.ID, Status: "succeeded", AmountCents: 1000, Currency: "EUR"})
	if _, err := f.sellers.HandleWebhook(f.ctx, wrong, sign(wrong)); !errors.Is(err, ErrInvalidPurchase) {
		t.Fatalf("wrong currency: %v", err)
	}
	for range 2 { // delivered twice, credited once
		got, err := f.sellers.HandleWebhook(f.ctx, body, sign(body))
		if err != nil || got.Status != models.PointsPurchaseCompleted {
			t.Fatalf("webhook: %+v %v", got, err)
		}
	}
	if w := f.sellerWallet(seller); w.Balance != 100 {
		t.Fatalf("webhook credited %d, want 100", w.Balance)
	}

	// Points come from what was actually paid.
	short, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 1000, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	partial, _ := json.Marshal(PaymentEvent{PurchaseID: short.ID, Status: "succeeded", AmountCents: 755, Currency: "USD"})
	got, err := f.sellers.HandleWebhook(f.ctx, partial, sign(partial))
	if err != nil || *got.PointsCredited != 75 || *got.PaidAmountCents != 755 {
		t.Fatalf("partial payment: %+v %v", got, err)
	}

	// One payment can only ever buy points once.
	again, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{AmountCents: 1000, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	reused, _ := json.Marshal(PaymentEvent{PurchaseID: again.ID, Status: "succeeded", AmountCents: 1000, Currency: "USD", ProviderReference: &ref})
	if _, err := f.sellers.HandleWebhook(f.ctx, reused, sign(reused)); !errors.Is(err, ErrInvalidPurchase) {
		t.Fatalf("reused payment reference: %v", err)
	}
	if w := f.sellerWallet(seller); w.Balance != 175 {
		t.Fatalf("balance %d, want 175", w.Balance)
	}
}

// ─── Product rewards ──────────────────────────────────────────────────────

func TestPointsProductRewardLifecycle(t *testing.T) {
	f := newPointsFixture(t, "manual")
	seller := f.seller()
	product := f.product(seller, 30)
	buyer := f.customer("")

	// Unfunded: sold without a reward, and nothing promised.
	o, err := f.order(buyer, product, 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if it := o.Items[0]; it.RewardStatus != "none" || it.RewardPoints != 0 {
		t.Fatalf("unfunded line: %+v", it)
	}

	f.fund(seller, 100)
	first, err := f.order(buyer, product, 2, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if it := first.Items[0]; it.RewardStatus != "reserved" || it.RewardPoints != 60 {
		t.Fatalf("funded line: %+v", it)
	}
	if w := f.sellerWallet(seller); w.Reserved != 60 || w.Available != 40 {
		t.Fatalf("after reserving: %+v", w)
	}
	// Only 40 left to promise, so two more units go without a reward.
	second, err := f.order(buyer, product, 2, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Items[0].RewardStatus != "none" {
		t.Fatalf("overcommitted: %+v", second.Items[0])
	}

	// Nothing is paid before delivery.
	f.run()
	if b := f.customerBalance(buyer); b != 0 {
		t.Fatalf("paid before delivery: %d", b)
	}

	// A third order is cancelled: its reservation goes back.
	third, err := f.order(buyer, product, 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.Cancel(f.ctx, buyer.String(), third.ID.String()); err != nil {
		t.Fatal(err)
	}
	if w := f.sellerWallet(seller); w.Reserved != 60 {
		t.Fatalf("cancel did not release: %+v", w)
	}

	f.deliver(first.ID)
	res := f.run()
	if res.ProductRewards != 1 || res.ProductRewardPoints != 60 {
		t.Fatalf("run: %+v", res)
	}
	f.run() // a second pass pays nothing more
	if b := f.customerBalance(buyer); b != 60 {
		t.Fatalf("customer balance %d, want 60", b)
	}
	w := f.sellerWallet(seller)
	if w.Balance != 40 || w.Reserved != 0 || w.LifetimeSpent != 60 {
		t.Fatalf("seller after payout: %+v", w)
	}
	wallet, err := f.repo.Wallet(f.ctx, buyer, 50)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Totals.FromPurchases != 60 || wallet.Entries[0].Description != "Purchased Wireless Headphones" ||
		wallet.Entries[0].Category != models.PointsCategoryProductPurchase {
		t.Fatalf("customer wallet: %+v", wallet)
	}

	// Refunded after delivery: taken back from the customer, returned to
	// the seller.
	f.exec(`update marketplace.orders set status = 'refunded' where id = $1`, first.ID)
	if res := f.run(); res.RewardsReversed != 1 {
		t.Fatalf("reversal run: %+v", res)
	}
	if b := f.customerBalance(buyer); b != 0 {
		t.Fatalf("customer after refund: %d", b)
	}
	if w := f.sellerWallet(seller); w.Balance != 100 {
		t.Fatalf("seller after refund: %+v", w)
	}
}

func TestPointsRewardReversalOnlyTakesWhatIsHeld(t *testing.T) {
	f := newPointsFixture(t, "manual")
	seller := f.seller()
	f.fund(seller, 100)
	buyer := f.customer("")
	o, err := f.order(buyer, f.product(seller, 50), 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.deliver(o.ID)
	f.run()
	// The customer spends 30 of the 50 before the refund.
	reason := "spent"
	if _, err := f.repo.Adjust(f.ctx, repository.PointsChange{
		CustomerID: buyer, EntryType: models.PointsEntryAdminDeduction, Delta: -30,
		IdempotencyKey: "test:" + uuid.NewString(), Reason: &reason, ActorType: "admin",
	}, models.AuditEntry{ActorType: "admin", Action: "points.adjusted", EntityType: "customer", EntityID: &buyer}); err != nil {
		t.Fatal(err)
	}
	f.exec(`update marketplace.orders set status = 'refunded' where id = $1`, o.ID)
	f.run()
	if b := f.customerBalance(buyer); b != 0 {
		t.Fatalf("balance went to %d", b)
	}
	if w := f.sellerWallet(seller); w.Balance != 70 {
		t.Fatalf("seller got back %d, want 20 (70 total)", w.Balance)
	}
}

func TestPointsConcurrentOrdersNeverOverReserve(t *testing.T) {
	f := newPointsFixture(t, "manual")
	seller := f.seller()
	f.fund(seller, 100)
	product := f.product(seller, 10)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.order(f.customer(""), product, 1, nil, 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	w := f.sellerWallet(seller)
	if w.Reserved != 100 || w.Available != 0 {
		t.Fatalf("20 orders against 100 points: %+v", w)
	}
}

// ─── Gift points ──────────────────────────────────────────────────────────

func TestPointsGiftDeliveredToRecipientAccount(t *testing.T) {
	f := newPointsFixture(t, "manual")
	seller := f.seller()
	product := f.product(seller, 0)
	sender := f.customer("")
	f.grant(sender, 60)
	email := uuid.NewString() + "@friend.test"
	friend := f.customer(email)
	rec := f.recipient(sender, &email)

	// More than the sender holds: refused, and nothing is written.
	if _, err := f.order(sender, product, 1, &rec, 100); !errors.Is(err, ErrGiftPointsBalance) {
		t.Fatalf("want ErrGiftPointsBalance, got %v", err)
	}
	if b := f.customerBalance(sender); b != 60 {
		t.Fatalf("refused order moved points: %d", b)
	}
	// No recipient email: refused.
	noEmail := f.recipient(sender, nil)
	if _, err := f.order(sender, product, 1, &noEmail, 10); !errors.Is(err, ErrGiftPoints) {
		t.Fatalf("want ErrGiftPoints, got %v", err)
	}

	o, err := f.order(sender, product, 1, &rec, 50)
	if err != nil {
		t.Fatal(err)
	}
	if o.GiftPointsStatus != "held" || f.customerBalance(sender) != 10 {
		t.Fatalf("gift not held: %s, sender %d", o.GiftPointsStatus, f.customerBalance(sender))
	}
	f.deliver(o.ID)
	if res := f.run(); res.GiftPointsDelivered != 1 {
		t.Fatalf("run: %+v", res)
	}
	f.run()
	if b := f.customerBalance(friend); b != 50 {
		t.Fatalf("friend got %d, want 50", b)
	}
	wallet, err := f.repo.Wallet(f.ctx, friend, 10)
	if err != nil {
		t.Fatal(err)
	}
	e := wallet.Entries[0]
	if e.Category != models.PointsCategoryGiftReward || e.Description != "Gift received" || e.OrderNumber != nil ||
		wallet.Totals.FromGifts != 50 {
		t.Fatalf("friend's entry: %+v totals %+v", e, wallet.Totals)
	}
}

func TestPointsGiftReturnedWhenUndeliverable(t *testing.T) {
	f := newPointsFixture(t, "manual")
	product := f.product(f.seller(), 0)
	sender := f.customer("")
	f.grant(sender, 100)
	stranger := uuid.NewString() + "@nobody.test"
	rec := f.recipient(sender, &stranger)

	// Delivered, but the recipient has no account: back to the sender.
	a, err := f.order(sender, product, 1, &rec, 40)
	if err != nil {
		t.Fatal(err)
	}
	f.deliver(a.ID)
	f.run()
	// Cancelled before delivery: back straight away.
	b, err := f.order(sender, product, 1, &rec, 25)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.Cancel(f.ctx, sender.String(), b.ID.String()); err != nil {
		t.Fatal(err)
	}
	if bal := f.customerBalance(sender); bal != 100 {
		t.Fatalf("sender has %d, want all 100 back", bal)
	}
	wallet, err := f.repo.Wallet(f.ctx, sender, 10)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Totals.SentAsGifts != 0 {
		t.Fatalf("net sent should be 0: %+v", wallet.Totals)
	}
}

// ─── Points prizes ────────────────────────────────────────────────────────

func TestPointsPrizeCreditedOnValidation(t *testing.T) {
	f := newPrizeFixture(t)
	rules := "Highest score wins."
	start := int64(0)
	prize := int64(250)
	view, err := f.svc.CreateCompetition(f.ctx, f.admin, CompetitionInput{
		CountryID: f.country.String(), GameSlug: "2048", Title: "Points prize round",
		StartsAt: time.Now().Add(time.Hour), EndsAt: time.Now().Add(48 * time.Hour), Timezone: "UTC",
		PointsPerAttempt: 5, MaxAttemptsPerCustomer: 5, NumberOfWinners: 1,
		PrizeDescription: "250 points", OfficialRules: &rules, PrizeType: "points", PrizePoints: &prize,
		StartPrizeCents: &start,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// No money reserve is needed to open a points prize.
	if _, err := f.svc.ScheduleCompetition(f.ctx, f.admin, view.ID); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	f.exec(`update competition.competitions set starts_at = now() - interval '1 minute' where id = $1`, view.ID)
	c := f.round(view.ID)
	player := f.customer(20)
	play, err := f.play(c, player, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	s, err := f.svc.games.GetSession(f.ctx, play.Session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SubmitOfficial(f.ctx, s, SubmitScoreInput{Moves: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CloseCompetition(f.ctx, f.admin, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.FreezeCompetition(f.ctx, f.admin, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.FinaliseCompetition(f.ctx, f.admin, c.ID, FinaliseInput{}); err != nil {
		t.Fatalf("finalise: %v", err)
	}
	winners, err := f.svc.AdminWinners(f.ctx, c.ID)
	if err != nil || len(winners) != 1 {
		t.Fatalf("winners: %v %v", winners, err)
	}
	if err := f.svc.ValidateWinner(f.ctx, f.admin, c.ID, winners[0].ID); err != nil {
		t.Fatal(err)
	}
	// 20 - 5 to play + 250 won.
	if b := f.balance(player); b != 265 {
		t.Fatalf("winner balance %d, want 265", b)
	}
	var claim string
	if err := f.pool.QueryRow(f.ctx, `select status from competition.prize_claims where winner_id = $1`,
		winners[0].ID).Scan(&claim); err != nil || claim != "fulfilled" {
		t.Fatalf("claim %q %v", claim, err)
	}
}

func (f *prizeFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("exec: %v\n%s", err, sql)
	}
}

// ─── Rewards paid when the order is placed ────────────────────────────────

func (f *pointsFixture) payAtOrder() {
	repo := repository.NewOrderRepository(f.pool)
	repo.PayRewardsAtOrder(true)
	f.orders = NewOrderService(repo, repository.NewCustomerRepository(f.pool),
		repository.NewCountryRepository(f.pool), repository.NewShipmentRepository(f.pool))
}

func TestPointsRewardPaidWhenOrderIsPlaced(t *testing.T) {
	f := newPointsFixture(t, "instant")
	f.payAtOrder()
	seller := f.seller()
	f.fund(seller, 100)
	buyer := f.customer("")
	o, err := f.order(buyer, f.product(seller, 30), 2, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if o.Items[0].RewardStatus != "awarded" || f.customerBalance(buyer) != 60 {
		t.Fatalf("line %s, buyer has %d", o.Items[0].RewardStatus, f.customerBalance(buyer))
	}
	if w := f.sellerWallet(seller); w.Balance != 40 || w.Reserved != 0 {
		t.Fatalf("seller: %+v", w)
	}
	// Nothing more is paid on delivery.
	f.deliver(o.ID)
	f.run()
	if b := f.customerBalance(buyer); b != 60 {
		t.Fatalf("paid twice: %d", b)
	}
}

func TestPointsCancelTakesBackOrderTimeRewards(t *testing.T) {
	f := newPointsFixture(t, "instant")
	f.payAtOrder()
	seller := f.seller()
	f.fund(seller, 100)
	product := f.product(seller, 30)
	buyer := f.customer("")

	o, err := f.order(buyer, product, 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.Cancel(f.ctx, buyer.String(), o.ID.String()); err != nil {
		t.Fatal(err)
	}
	if b := f.customerBalance(buyer); b != 0 {
		t.Fatalf("buyer kept %d after cancelling", b)
	}
	if w := f.sellerWallet(seller); w.Balance != 100 {
		t.Fatalf("seller not repaid: %+v", w)
	}

	// Spent before cancelling: the cancellation is refused and nothing moves.
	o2, err := f.order(buyer, product, 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	reason := "played a game"
	if _, err := f.repo.Adjust(f.ctx, repository.PointsChange{
		CustomerID: buyer, EntryType: models.PointsEntryAdminDeduction, Delta: -20,
		IdempotencyKey: "test:" + uuid.NewString(), Reason: &reason, ActorType: "admin",
	}, models.AuditEntry{ActorType: "admin", Action: "points.adjusted", EntityType: "customer", EntityID: &buyer}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.Cancel(f.ctx, buyer.String(), o2.ID.String()); !errors.Is(err, ErrOrderRewardSpent) {
		t.Fatalf("want ErrOrderRewardSpent, got %v", err)
	}
	got, err := f.orders.Get(f.ctx, buyer.String(), o2.ID.String())
	if err != nil || got.Status == "cancelled" || f.customerBalance(buyer) != 10 {
		t.Fatalf("refused cancel changed things: %v %v balance %d", got.Status, err, f.customerBalance(buyer))
	}
}

// ─── Paying to play ───────────────────────────────────────────────────────

func TestPointsGamePlayCostsPoints(t *testing.T) {
	f := newPointsFixture(t, "instant")
	svc := NewGameService(repository.NewGameRepository(f.pool))
	svc.ChargePerPlay(50, f.repo)
	player := f.customer("")

	// Not enough points: refused, and nothing recorded.
	_, err := svc.StartSession(f.ctx, "2048", SocialActor{CustomerID: player.String()}, 1)
	var refusal *PlayRefusal
	if !errors.As(err, &refusal) || refusal.Code != PlayInsufficientPoints || refusal.Details["points_balance"] != int64(0) {
		t.Fatalf("want INSUFFICIENT_POINTS, got %v", err)
	}
	// A guest cannot pay.
	if _, err := svc.StartSession(f.ctx, "2048", SocialActor{GuestToken: "guest-" + uuid.NewString()}, 1); !errors.As(err, &refusal) || refusal.Code != PlaySignInRequired {
		t.Fatalf("guest: %v", err)
	}

	f.grant(player, 120)
	view, err := svc.StartSession(f.ctx, "2048", SocialActor{CustomerID: player.String()}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if view.PointsCharged != 50 || view.PointsBalance == nil || *view.PointsBalance != 70 {
		t.Fatalf("session: charged %d, balance %v", view.PointsCharged, view.PointsBalance)
	}
	w, err := f.repo.Wallet(f.ctx, player, 10)
	if err != nil {
		t.Fatal(err)
	}
	e := w.Entries[0]
	if e.Category != models.PointsCategoryGameEntry || e.Description != "Played 2048" || w.Totals.SpentOnGames != 50 {
		t.Fatalf("ledger: %+v totals %+v", e, w.Totals)
	}
	// Two more plays: the second is refused at 20 points.
	if _, err := svc.StartSession(f.ctx, "2048", SocialActor{CustomerID: player.String()}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartSession(f.ctx, "2048", SocialActor{CustomerID: player.String()}, 1); !errors.As(err, &refusal) {
		t.Fatalf("third play: %v", err)
	}
	if b := f.customerBalance(player); b != 20 {
		t.Fatalf("balance %d, want 20", b)
	}
}
