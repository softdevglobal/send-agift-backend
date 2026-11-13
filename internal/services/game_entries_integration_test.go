package services

import (
	"context"
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

// Integration tests for advertising packages and game entries, run against
// a real Postgres because what is under test (row locks, one transaction,
// unique keys, check constraints) lives in the database.
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
//	    go test ./internal/services -run 'GameEntries|Advertising|State' -v
//
// Point it at a throwaway database: it migrates it and writes test rows.

type shopFixture struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	orders  *OrderService
	orderDB *repository.OrderRepository
	entries *GameEntriesService
	repo    *repository.GameEntriesRepository
	ads     *AdvertisingService
	admin   AdminActor
	country uuid.UUID
}

func newShopFixture(t *testing.T) *shopFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed tests")
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
	f := &shopFixture{t: t, ctx: ctx, pool: pool, admin: AdminActor{ID: uuid.New()}}
	iso := "P" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:10])
	f.scan(&f.country, `insert into core.countries (iso_code, name, default_currency, default_timezone)
		values ($1, $2, 'USD', 'UTC') returning id`, iso, "Entryland "+iso)
	f.orderDB = repository.NewOrderRepository(pool)
	f.orders = NewOrderService(f.orderDB, repository.NewCustomerRepository(pool),
		repository.NewCountryRepository(pool), repository.NewShipmentRepository(pool))
	f.repo = repository.NewGameEntriesRepository(pool)
	f.entries = NewGameEntriesService(f.repo, 90)
	// No Stripe: purchases are confirmed by the sandbox's test payments.
	f.ads = NewAdvertisingService(repository.NewAdRepository(pool), nil, nil, nil, "http://localhost:5173", true)
	return f
}

func (f *shopFixture) scan(dest any, sql string, args ...any) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(dest); err != nil {
		f.t.Fatalf("scan: %v\n%s", err, sql)
	}
}

func (f *shopFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("exec: %v\n%s", err, sql)
	}
}

func (f *shopFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	f.scan(&n, sql, args...)
	return n
}

func (f *shopFixture) seller() uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	f.scan(&id, `insert into seller.sellers (country_id, legal_name, email, password_hash)
		values ($1, 'Test Seller', $2, 'x') returning id`, f.country, uuid.NewString()+"@seller.test")
	return id
}

// shop makes an active shop for a seller, at a fixed point with a generous,
// free delivery zone, so any recipient address this fixture creates (also
// fixed, a few km away) can always be delivered to.
func (f *shopFixture) shop(seller uuid.UUID) uuid.UUID {
	f.t.Helper()
	var addr, shop uuid.UUID
	f.scan(&addr, `insert into seller.seller_addresses (seller_id, country_id, line1, city, latitude, longitude)
		values ($1, $2, '1 Test Road', 'Colombo', 6.9000, 79.8550) returning id`, seller, f.country)
	slug := "shop-" + uuid.NewString()[:8]
	f.scan(&shop, `insert into seller.shops (seller_id, country_id, name, slug, address_id, latitude, longitude, status)
		values ($1, $2, $3, $3, $4, 6.9000, 79.8550, 'active') returning id`, seller, f.country, slug, addr)
	f.exec(`insert into seller.shop_delivery_zones (shop_id, max_km, price_amount, currency, estimated_days)
		values ($1, 100, 0, 'USD', 1)`, shop)
	return shop
}

func (f *shopFixture) productIn(shop uuid.UUID) uuid.UUID {
	f.t.Helper()
	var product uuid.UUID
	f.scan(&product, `insert into seller.products (shop_id, name, slug, price_amount, currency, status)
		values ($1, 'Wireless Headphones', $2, 1000000, 'USD', 'published') returning id`,
		shop, "p-"+uuid.NewString()[:8])
	return product
}

// product makes a shop for the seller with one published product.
func (f *shopFixture) product(seller uuid.UUID) uuid.UUID {
	return f.productIn(f.shop(seller))
}

// customer makes an adult customer; email "" makes one up.
func (f *shopFixture) customer(email string) uuid.UUID {
	f.t.Helper()
	if email == "" {
		email = uuid.NewString() + "@customer.test"
	}
	var id uuid.UUID
	f.scan(&id, `insert into customer.customers (country_id, email, password_hash, display_name, date_of_birth)
		values ($1, $2, 'x', 'Test Customer', '1990-01-01') returning id`, f.country, email)
	return id
}

func (f *shopFixture) recipient(customer uuid.UUID) uuid.UUID {
	f.t.Helper()
	var id, addr uuid.UUID
	f.scan(&id, `insert into customer.recipients (customer_id, name) values ($1, 'Friend') returning id`, customer)
	f.scan(&addr, `insert into customer.recipient_addresses
		(recipient_id, country_id, line1, city, postal_code, latitude, longitude, is_default)
		values ($1, $2, '1 Galle Road', 'Colombo', '00300', 6.9271, 79.8612, true) returning id`,
		id, f.country)
	f.exec(`update customer.recipients set default_address_id = $2 where id = $1`, id, addr)
	return id
}

// order places an order for a customer. It is unpaid until pay().
func (f *shopFixture) order(customer, product uuid.UUID, qty int) (*models.OrderDetails, error) {
	rid := f.recipient(customer).String()
	return f.orders.Create(f.ctx, customer.String(), OrderCreateInput{
		CountryID: f.country.String(), DeliveryDate: time.Now().AddDate(0, 0, 3).Format("2006-01-02"),
		RecipientID: &rid,
		Items:       []OrderItemInput{{ProductID: product.String(), Quantity: qty}},
	})
}

func (f *shopFixture) mustOrder(customer, product uuid.UUID) *models.OrderDetails {
	f.t.Helper()
	o, err := f.order(customer, product, 1)
	if err != nil {
		f.t.Fatalf("order: %v", err)
	}
	return o
}

// pay is the payment provider confirming an order's payment.
func (f *shopFixture) pay(order uuid.UUID) {
	f.t.Helper()
	f.exec(`update marketplace.orders set status = 'paid', updated_at = now() where id = $1`, order)
	if err := f.orderDB.GrantEntriesForPaidOrder(f.ctx, order); err != nil {
		f.t.Fatalf("grant: %v", err)
	}
}

func (f *shopFixture) refund(order uuid.UUID) {
	f.t.Helper()
	f.exec(`update marketplace.orders set status = 'refunded', updated_at = now() where id = $1`, order)
	if _, err := f.repo.Settle(f.ctx, 500); err != nil {
		f.t.Fatalf("settle: %v", err)
	}
}

func (f *shopFixture) balance(customer uuid.UUID) int64 {
	f.t.Helper()
	b, err := f.repo.Balance(f.ctx, customer)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

// open switches games and entry earning on for the fixture's country.
func (f *shopFixture) open() { openGamesFor(f.t, f.pool, f.country) }

// pkg creates and publishes a package with these level awards and pool.
func (f *shopFixture) pkg(name string, priceMinor int64, levels []int, pool int) *models.AdPackage {
	f.t.Helper()
	p, err := f.ads.CreatePackage(f.ctx, f.admin, AdPackageInput{
		CountryID: f.country.String(), Name: name, Currency: "USD", PriceMinor: priceMinor,
		LevelEntries: levels, EntriesPool: pool, ValidityDays: 30,
	})
	if err != nil {
		f.t.Fatalf("create package: %v", err)
	}
	if p, err = f.ads.PackageAction(f.ctx, f.admin, p.ID, "publish"); err != nil {
		f.t.Fatalf("publish package: %v", err)
	}
	return p
}

// buy has the seller buy a package and the payment confirmed.
func (f *shopFixture) buy(seller uuid.UUID, pkg *models.AdPackage) *models.AdPurchase {
	f.t.Helper()
	p, err := f.ads.Buy(f.ctx, seller.String(), BuyInput{PackageID: pkg.ID.String(), IdempotencyKey: uuid.NewString()})
	if err != nil {
		f.t.Fatalf("buy: %v", err)
	}
	if p.Status != models.AdPurchasePending {
		f.t.Fatalf("a purchase must wait for its payment, got %s", p.Status)
	}
	if p, err = f.ads.TestPay(f.ctx, seller.String(), p.ID); err != nil {
		f.t.Fatalf("pay: %v", err)
	}
	return p
}

func (f *shopFixture) promote(seller uuid.UUID, purchase uuid.UUID, products ...uuid.UUID) *models.AdPurchase {
	f.t.Helper()
	in := make([]PromotionInput, len(products))
	for i, p := range products {
		in[i] = PromotionInput{ProductID: p.String(), Level: i + 1}
	}
	out, err := f.ads.SetPromotions(f.ctx, seller.String(), purchase, in)
	if err != nil {
		f.t.Fatalf("promote: %v", err)
	}
	return out
}

func (f *shopFixture) purchase(seller, id uuid.UUID) *models.AdPurchase {
	f.t.Helper()
	p, err := repository.NewAdRepository(f.pool).GetSellerPurchase(f.ctx, seller, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// An admin creates the $50 package with 3 levels and the $100 package with
// 5, each with their own numbers; a seller buys one and promotes products
// in its levels; a published package cannot be changed, only versioned.
func TestAdvertisingPackagesAndPromotion(t *testing.T) {
	f := newShopFixture(t)
	fifty := f.pkg("Starter", 5000, []int{3, 2, 1}, 30)
	hundred := f.pkg("Growth", 10000, []int{5, 4, 3, 2, 1}, 100)
	if fifty.ProductSlots != 3 || hundred.ProductSlots != 5 {
		t.Fatalf("slots = %d and %d", fifty.ProductSlots, hundred.ProductSlots)
	}

	// Nonsense is refused: a lower level paying more, or a pool that cannot
	// pay one top award.
	for _, bad := range []AdPackageInput{
		{CountryID: f.country.String(), Name: "x", Currency: "USD", PriceMinor: 1, LevelEntries: []int{1, 2}, EntriesPool: 10, ValidityDays: 30},
		{CountryID: f.country.String(), Name: "x", Currency: "USD", PriceMinor: 1, LevelEntries: []int{5}, EntriesPool: 4, ValidityDays: 30},
		{CountryID: f.country.String(), Name: "x", Currency: "USD", PriceMinor: 1, LevelEntries: nil, EntriesPool: 4, ValidityDays: 30},
	} {
		if _, err := f.ads.CreatePackage(f.ctx, f.admin, bad); !errors.Is(err, ErrInvalidAdPackage) {
			t.Fatalf("bad package accepted: %+v (%v)", bad, err)
		}
	}

	// A published version is frozen, in the service and in the database.
	if _, err := f.ads.UpdatePackage(f.ctx, f.admin, fifty.ID, AdPackageInput{
		CountryID: f.country.String(), Name: "Starter", Currency: "USD", PriceMinor: 1,
		LevelEntries: []int{9, 9, 9}, EntriesPool: 30, ValidityDays: 30}); !errors.Is(err, ErrAdPackageState) {
		t.Fatalf("published package edited: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `update ads.packages set price_minor = 1 where id = $1`, fifty.ID); err == nil {
		t.Fatalf("the database let a published package change")
	}

	seller := f.seller()
	shop := f.shop(seller)
	a, b, c, d := f.productIn(shop), f.productIn(shop), f.productIn(shop), f.productIn(shop)
	bought := f.buy(seller, fifty)
	if bought.Status != models.AdPurchaseActive || bought.ExpiresAt == nil || bought.EntriesPool != 30 {
		t.Fatalf("purchase = %+v", bought)
	}

	// The package limits how many products, and only the seller's own.
	if _, err := f.ads.SetPromotions(f.ctx, seller.String(), bought.ID, []PromotionInput{
		{ProductID: a.String(), Level: 1}, {ProductID: b.String(), Level: 2},
		{ProductID: c.String(), Level: 3}, {ProductID: d.String(), Level: 4}}); !errors.Is(err, ErrInvalidAdPurchase) {
		t.Fatalf("a fourth product on a 3-product package: %v", err)
	}
	stranger := f.product(f.seller())
	if _, err := f.ads.SetPromotions(f.ctx, seller.String(), bought.ID, []PromotionInput{
		{ProductID: stranger.String(), Level: 1}}); !errors.Is(err, ErrInvalidAdPurchase) {
		t.Fatalf("another seller's product promoted: %v", err)
	}

	list := f.promote(seller, bought.ID, a, b, c)
	if len(list.Promotions) != 3 || list.Promotions[0].EntriesPerPurchase != 3 ||
		list.Promotions[1].EntriesPerPurchase != 2 || list.Promotions[2].EntriesPerPurchase != 1 {
		t.Fatalf("promotions = %+v", list.Promotions)
	}
	// Reordering while active: c moves to the top.
	list = f.promote(seller, bought.ID, c, a, b)
	if list.Promotions[0].ProductID != c || list.Promotions[0].EntriesPerPurchase != 3 {
		t.Fatalf("after reorder = %+v", list.Promotions)
	}

	// The admin changes the package through a new version; the seller's
	// purchase keeps the terms it was bought on.
	v2, err := f.ads.PackageAction(f.ctx, f.admin, fifty.ID, "new-version")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ads.UpdatePackage(f.ctx, f.admin, v2.ID, AdPackageInput{
		CountryID: f.country.String(), Name: "Starter", Currency: "USD", PriceMinor: 6000,
		LevelEntries: []int{6, 4, 2}, EntriesPool: 60, ValidityDays: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ads.PackageAction(f.ctx, f.admin, v2.ID, "publish"); err != nil {
		t.Fatal(err)
	}
	if got := f.purchase(seller, bought.ID); got.EntriesPool != 30 || got.LevelEntries[0] != 3 || got.PriceMinor != 5000 {
		t.Fatalf("an existing purchase changed terms: %+v", got)
	}
	dash, err := f.ads.Dashboard(f.ctx, seller.String())
	if err != nil {
		t.Fatal(err)
	}
	onSale := 0
	for _, p := range dash.Packages {
		if p.PackageKey == fifty.PackageKey {
			onSale++
			if p.Version != 2 {
				t.Fatalf("the old version is still on sale")
			}
		}
	}
	if onSale != 1 {
		t.Fatalf("%d versions of one package on sale", onSale)
	}
}

// A payment that is not the package's price and currency activates nothing,
// and a confirmation sent twice counts once.
func TestAdvertisingPaymentMustMatch(t *testing.T) {
	f := newShopFixture(t)
	pkg := f.pkg("Starter", 5000, []int{3, 2, 1}, 30)
	seller := f.seller()
	p, err := f.ads.Buy(f.ctx, seller.String(), BuyInput{PackageID: pkg.ID.String(), IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.ads.Buy(f.ctx, seller.String(), BuyInput{PackageID: pkg.ID.String(), IdempotencyKey: "k1"})
	if err != nil || again.ID != p.ID {
		t.Fatalf("the same key made a second purchase: %v", err)
	}
	session := func(amount int64, currency, sellerID string) StripeCheckoutSession {
		return StripeCheckoutSession{ID: "cs_test_" + p.ID.String(), PaymentStatus: "paid", AmountTotal: amount,
			Currency: currency, Metadata: map[string]string{"purchase_id": p.ID.String(), "seller_id": sellerID}}
	}
	for name, cs := range map[string]StripeCheckoutSession{
		"wrong amount":   session(4999, "usd", seller.String()),
		"wrong currency": session(5000, "eur", seller.String()),
		"wrong seller":   session(5000, "usd", uuid.NewString()),
	} {
		if err := f.ads.HandleStripeCheckout(f.ctx, cs); err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if got := f.purchase(seller, p.ID); got.Status != models.AdPurchasePending {
			t.Fatalf("%s: purchase is %s", name, got.Status)
		}
	}
	unpaid := session(5000, "usd", seller.String())
	unpaid.PaymentStatus = "unpaid"
	if err := f.ads.HandleStripeCheckout(f.ctx, unpaid); err != nil {
		t.Fatal(err)
	}
	if got := f.purchase(seller, p.ID); got.Status != models.AdPurchasePending {
		t.Fatalf("an unpaid session activated the package")
	}
	good := session(5000, "usd", seller.String())
	for i := 0; i < 2; i++ {
		if err := f.ads.HandleStripeCheckout(f.ctx, good); err != nil {
			t.Fatal(err)
		}
	}
	got := f.purchase(seller, p.ID)
	if got.Status != models.AdPurchaseActive || got.EntriesUsed != 0 {
		t.Fatalf("purchase = %+v", got)
	}
}

// Entries are granted when the payment is confirmed, not before; they count
// against the package at that moment; a refund takes them back.
func TestGameEntriesGrantedOnPaymentAndReversedOnRefund(t *testing.T) {
	f := newShopFixture(t)
	f.open()
	seller := f.seller()
	shop := f.shop(seller)
	high, low := f.productIn(shop), f.productIn(shop)
	bought := f.buy(seller, f.pkg("Starter", 5000, []int{3, 1}, 30))
	f.promote(seller, bought.ID, high, low)
	customer := f.customer("")

	offers, err := f.entries.Offers(f.ctx, customer, []string{high.String(), low.String()})
	if err != nil || offers.Total != 4 {
		t.Fatalf("offers = %+v (%v)", offers, err)
	}

	o := f.mustOrder(customer, high)
	if o.Items[0].GameEntries != 3 || o.Items[0].EntriesStatus != "reserved" {
		t.Fatalf("line = %+v", o.Items[0])
	}
	if f.balance(customer) != 0 {
		t.Fatalf("entries granted before payment")
	}
	if p := f.purchase(seller, bought.ID); p.EntriesHeld != 3 || p.EntriesUsed != 0 {
		t.Fatalf("before payment: %+v", p)
	}
	w, _ := f.entries.Wallet(f.ctx, customer)
	if w.Totals.Pending != 3 {
		t.Fatalf("pending = %d", w.Totals.Pending)
	}

	f.pay(o.ID)
	f.pay(o.ID) // a repeated confirmation grants nothing more
	if f.balance(customer) != 3 {
		t.Fatalf("balance = %d", f.balance(customer))
	}
	if p := f.purchase(seller, bought.ID); p.EntriesHeld != 0 || p.EntriesUsed != 3 || p.UsagePercent != 10 {
		t.Fatalf("after payment: %+v", p)
	}

	// An abandoned checkout never grants: cancelling releases the promise.
	abandoned := f.mustOrder(customer, low)
	if _, err := f.orders.Cancel(f.ctx, customer.String(), abandoned.ID.String()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if p := f.purchase(seller, bought.ID); p.EntriesHeld != 0 || p.EntriesUsed != 3 {
		t.Fatalf("after abandoned checkout: %+v", p)
	}
	if f.balance(customer) != 3 {
		t.Fatalf("an unpaid order granted entries")
	}

	// Refund: the entries come back, and the seller's budget with them.
	f.refund(o.ID)
	if f.balance(customer) != 0 {
		t.Fatalf("balance after refund = %d", f.balance(customer))
	}
	if p := f.purchase(seller, bought.ID); p.EntriesUsed != 0 {
		t.Fatalf("budget after refund: %+v", p)
	}
	if n := f.count(`select count(*) from entries.shortfalls where customer_id = $1`, customer); n != 0 {
		t.Fatalf("a shortfall was recorded for a full recovery")
	}
}

// A refund after the entries were used takes what is left, records the
// rest for review, and never pushes the balance below zero.
func TestGameEntriesRefundAfterSpending(t *testing.T) {
	f := newShopFixture(t)
	f.open()
	seller := f.seller()
	product := f.product(seller)
	bought := f.buy(seller, f.pkg("Starter", 5000, []int{5}, 50))
	f.promote(seller, bought.ID, product)
	customer := f.customer("")
	o := f.mustOrder(customer, product)
	f.pay(o.ID)
	// The customer plays 4 of the 5.
	if _, err := f.entries.Adjust(f.ctx, f.admin, customer, AdjustEntriesInput{
		Amount: -4, Reason: "stand-in for plays", IdempotencyKey: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	f.refund(o.ID)
	if f.balance(customer) != 0 {
		t.Fatalf("balance = %d", f.balance(customer))
	}
	cases, err := f.entries.Shortfalls(f.ctx, "open")
	if err != nil {
		t.Fatal(err)
	}
	var mine *models.EntryShortfall
	for i := range cases {
		if cases[i].CustomerID == customer {
			mine = &cases[i]
		}
	}
	if mine == nil || mine.EntriesShort != 4 || mine.EntriesRecovered != 1 {
		t.Fatalf("shortfall = %+v", mine)
	}
	if err := f.entries.ResolveShortfall(f.ctx, f.admin, mine.ID, "written off"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `update entries.accounts set balance = -1 where customer_id = $1`, customer); err == nil {
		t.Fatalf("the database allowed a negative balance")
	}
}

// Reminders at 80, 85, 90, 95 and 100%, each once; several passed at once
// send only the highest; at 100% the products leave the list.
func TestAdvertisingBudgetReminders(t *testing.T) {
	f := newShopFixture(t)
	f.open()
	seller := f.seller()
	product := f.product(seller)
	// 20 purchases of 5 entries use the 100-entry pool: 5% each.
	bought := f.buy(seller, f.pkg("Growth", 10000, []int{5}, 100))
	f.promote(seller, bought.ID, product)

	thresholds := func() []int {
		rows, err := f.pool.Query(f.ctx, `
			select threshold from ads.reminders where purchase_id = $1 and kind = 'auto' order by created_at, threshold`, bought.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int
		for rows.Next() {
			var n int
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		return out
	}
	buyOnce := func() uuid.UUID {
		o := f.mustOrder(f.customer(""), product)
		f.pay(o.ID)
		return o.ID
	}
	var orders []uuid.UUID
	for i := 0; i < 15; i++ { // 75%
		orders = append(orders, buyOnce())
	}
	if got := thresholds(); len(got) != 0 {
		t.Fatalf("reminders below 80%%: %v", got)
	}
	want := []int{80, 85, 90, 95, 100}
	for i := 0; i < 5; i++ {
		orders = append(orders, buyOnce())
		got := thresholds()
		if len(got) != i+1 || got[i] != want[i] {
			t.Fatalf("after %d%%: reminders = %v", 80+5*i, got)
		}
	}
	p := f.purchase(seller, bought.ID)
	if p.Status != models.AdPurchaseExhausted || p.UsagePercent != 100 {
		t.Fatalf("purchase = %+v", p)
	}
	// The products have left the list: nothing more is promised.
	if o := f.mustOrder(f.customer(""), product); o.Items[0].GameEntries != 0 {
		t.Fatalf("an exhausted package still promised entries")
	}
	var sponsored bool
	f.scan(&sponsored, `select exists(select 1 from ads.promotions pr inner join ads.purchases pu on pu.id = pr.purchase_id
		where pr.product_id = $1 and pr.removed_at is null and pu.status = 'active')`, product)
	if sponsored {
		t.Fatalf("the product is still on the promoted list")
	}

	// A refund does not send a mark again when usage passes it a second time.
	f.refund(orders[len(orders)-1])
	if p := f.purchase(seller, bought.ID); p.Status != models.AdPurchaseActive || p.EntriesUsed != 95 {
		t.Fatalf("after refund: %+v", p)
	}
	buyOnce()
	if got := thresholds(); len(got) != 5 {
		t.Fatalf("a reminder was sent twice: %v", got)
	}

	// The admin sees the usage and can send the notice again.
	list, err := f.ads.AdminPurchases(f.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, row := range list {
		if row.ID == bought.ID {
			seen = row.UsagePercent == 100
		}
	}
	if !seen {
		t.Fatalf("admin does not see the purchase at 100%%")
	}
	rem, err := f.ads.ResendReminder(f.ctx, f.admin, bought.ID)
	if err != nil || rem.Threshold != 100 || rem.Kind != "resend" {
		t.Fatalf("resend = %+v (%v)", rem, err)
	}
	dash, err := f.ads.Dashboard(f.ctx, seller.String())
	if err != nil || len(dash.Reminders) != 6 {
		t.Fatalf("seller sees %d reminders (%v)", len(dash.Reminders), err)
	}

	// Passing several marks at once sends only the highest.
	jump := f.seller()
	jp := f.product(jump)
	big := f.buy(jump, f.pkg("Jump", 10000, []int{9}, 10))
	f.promote(jump, big.ID, jp)
	o := f.mustOrder(f.customer(""), jp)
	f.pay(o.ID) // 90% in one step, and nothing left for another award
	var marks []int
	rows, _ := f.pool.Query(f.ctx, `select threshold from ads.reminders where purchase_id = $1`, big.ID)
	for rows.Next() {
		var n int
		_ = rows.Scan(&n)
		marks = append(marks, n)
	}
	rows.Close()
	if len(marks) != 1 || marks[0] != 100 {
		t.Fatalf("a jump past several marks sent %v", marks)
	}
}

// Concurrent checkouts cannot promise more than the package holds.
func TestAdvertisingBudgetNeverOverdrawn(t *testing.T) {
	f := newShopFixture(t)
	f.open()
	seller := f.seller()
	product := f.product(seller)
	bought := f.buy(seller, f.pkg("Tiny", 5000, []int{2}, 6)) // three awards
	f.promote(seller, bought.ID, product)
	const n = 8
	customers := make([]uuid.UUID, n)
	for i := range customers {
		customers[i] = f.customer("")
	}
	var wg sync.WaitGroup
	got := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, err := f.order(customers[i], product, 1)
			if err != nil {
				t.Errorf("order: %v", err)
				return
			}
			got[i] = o.Items[0].GameEntries
			f.pay(o.ID)
		}(i)
	}
	wg.Wait()
	total := 0
	for _, g := range got {
		if g != 0 && g != 2 {
			t.Fatalf("a part award of %d", g)
		}
		total += g
	}
	p := f.purchase(seller, bought.ID)
	if total != 6 || p.EntriesUsed != 6 || p.EntriesHeld != 0 || p.Status != models.AdPurchaseExhausted {
		t.Fatalf("promised %d; purchase = %+v", total, p)
	}
}

// A customer where earning is not switched on, or who is the seller, earns
// nothing; daily caps hold; a stop ends earning at once.
func TestGameEntriesGateAndCaps(t *testing.T) {
	f := newShopFixture(t)
	seller := f.seller()
	product := f.product(seller)
	bought := f.buy(seller, f.pkg("Starter", 5000, []int{2}, 100))
	f.promote(seller, bought.ID, product)

	// Nothing is switched on for this country yet.
	shut := f.customer("")
	if o := f.mustOrder(shut, product); o.Items[0].GameEntries != 0 {
		t.Fatalf("an unapproved country earned entries")
	}
	if _, err := f.entries.Adjust(f.ctx, f.admin, shut, AdjustEntriesInput{Amount: 5, Reason: "test", IdempotencyKey: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	w, err := f.entries.Wallet(f.ctx, shut)
	if err != nil || w.Earn.Allowed || w.Play.Allowed {
		t.Fatalf("gate open with nothing switched on: %+v", w)
	}

	f.open()
	// The seller buying their own product earns nothing.
	var sellerEmail string
	f.scan(&sellerEmail, `select email::text from seller.sellers where id = $1`, seller)
	own := f.customer(sellerEmail)
	if o := f.mustOrder(own, product); o.Items[0].GameEntries != 0 {
		t.Fatalf("a seller earned from their own product")
	}
	// Under age, and no date of birth.
	young := f.customer("")
	f.exec(`update customer.customers set date_of_birth = now() - interval '16 years' where id = $1`, young)
	if o := f.mustOrder(young, product); o.Items[0].GameEntries != 0 {
		t.Fatalf("an under-age customer earned entries")
	}

	// Caps: 4 a day overall, 2 a day from one product.
	f.exec(`update entries.jurisdictions set customer_daily_cap = 4, product_daily_cap = 2 where country_id = $1`, f.country)
	capped := f.customer("")
	if o := f.mustOrder(capped, product); o.Items[0].GameEntries != 2 {
		t.Fatalf("first purchase earned %d", o.Items[0].GameEntries)
	}
	if o := f.mustOrder(capped, product); o.Items[0].GameEntries != 0 {
		t.Fatalf("the per-product cap was passed")
	}
	other := f.productIn(f.shop(seller))
	second := f.buy(f.seller(), f.pkg("Other", 5000, []int{2}, 100))
	_ = second
	f.exec(`update entries.jurisdictions set product_daily_cap = null where country_id = $1`, f.country)
	_ = other
	if o := f.mustOrder(capped, product); o.Items[0].GameEntries != 2 {
		t.Fatalf("second purchase under the overall cap earned %d", o.Items[0].GameEntries)
	}
	if o := f.mustOrder(capped, product); o.Items[0].GameEntries != 0 {
		t.Fatalf("the customer cap was passed")
	}
	f.exec(`update entries.jurisdictions set customer_daily_cap = null where country_id = $1`, f.country)

	// A stop between checkout and payment: the promise is released.
	late := f.customer("")
	o := f.mustOrder(late, product)
	if o.Items[0].GameEntries != 2 {
		t.Fatalf("earned %d", o.Items[0].GameEntries)
	}
	var jid uuid.UUID
	f.scan(&jid, `select id from entries.jurisdictions where country_id = $1`, f.country)
	if _, err := f.entries.StopJurisdiction(f.ctx, f.admin, jid, true, "incident drill"); err != nil {
		t.Fatal(err)
	}
	f.pay(o.ID)
	if f.balance(late) != 0 {
		t.Fatalf("entries granted during a stop")
	}
	if got := f.mustOrder(f.customer(""), product); got.Items[0].GameEntries != 0 {
		t.Fatalf("entries promised during a stop")
	}
	if _, err := f.entries.StopJurisdiction(f.ctx, f.admin, jid, false, "drill over"); err != nil {
		t.Fatal(err)
	}
	if got := f.mustOrder(f.customer(""), product); got.Items[0].GameEntries != 2 {
		t.Fatalf("earning did not come back after the stop was lifted")
	}
}

// Sign-up must give a state where the country has states; a state can be
// set once freely and then only after the cooldown; a competition for one
// state is hidden from, and cannot be played by, customers in another.
func TestStateAtSignupAndStateCompetitions(t *testing.T) {
	f := newPrizeFixture(t)
	entries := repository.NewGameEntriesRepository(f.pool)
	svc := NewGameEntriesService(entries, 90)
	var north, south uuid.UUID
	for name, dest := range map[string]*uuid.UUID{"North": &north, "South": &south} {
		st, err := svc.SaveState(f.ctx, f.admin, nil, StateInput{CountryID: f.country.String(), Code: strings.ToUpper(name[:2]), Name: name})
		if err != nil {
			t.Fatal(err)
		}
		*dest = st.ID
	}
	list, err := svc.States(f.ctx, f.country.String())
	if err != nil || len(list) != 2 {
		t.Fatalf("states = %v (%v)", list, err)
	}

	// Sign-up, by form or through Google, Facebook or Apple, runs this check.
	if _, err := svc.RequireStateForSignup(f.ctx, f.country, ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("sign-up without a state: %v", err)
	}
	if _, err := svc.RequireStateForSignup(f.ctx, f.country, uuid.NewString()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("sign-up with a state of another country: %v", err)
	}
	if id, err := svc.RequireStateForSignup(f.ctx, f.country, north.String()); err != nil || id == nil || *id != north {
		t.Fatalf("sign-up with a state: %v", err)
	}

	// An existing customer has no state: they are asked, and until they
	// answer they cannot play.
	northerner, southerner := f.customer(50), f.customer(50)
	st, err := svc.CustomerState(f.ctx, northerner)
	if err != nil || !st.StateRequired {
		t.Fatalf("existing customer not asked for a state: %+v (%v)", st, err)
	}
	gate, _ := entries.Gate(f.ctx, northerner, "play", nil)
	if gate.Allowed || gate.Code != models.GateStateRequired {
		t.Fatalf("gate without a state = %+v", gate)
	}
	if _, err := svc.SetCustomerState(f.ctx, northerner, north.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetCustomerState(f.ctx, southerner, south.String()); err != nil {
		t.Fatal(err)
	}
	// Switching state to enter a competition is refused.
	if _, err := svc.SetCustomerState(f.ctx, southerner, north.String()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("a state was changed inside the cooldown: %v", err)
	}

	// The country now has states, so the country-wide setting made by the
	// fixture no longer covers anyone: games are on only in North.
	gate, _ = entries.Gate(f.ctx, northerner, "play", nil)
	if gate.Allowed {
		t.Fatalf("a state inherited its country's approval")
	}
	var gamesOn []string
	rows, _ := f.pool.Query(f.ctx, `select id::text from competition.games`)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		gamesOn = append(gamesOn, id)
	}
	rows.Close()
	rules := "Official rules for North."
	northID := north.String()
	if _, err := svc.SaveJurisdiction(f.ctx, f.admin, JurisdictionInput{
		CountryID: f.country.String(), EarningEnabled: true, PlayEnabled: true, OfficialRules: &rules,
	}); !errors.Is(err, ErrInvalidEntries) {
		t.Fatalf("a country with states was switched on as a whole: %v", err)
	}
	if _, err := svc.SaveJurisdiction(f.ctx, f.admin, JurisdictionInput{
		CountryID: f.country.String(), StateID: &northID, EarningEnabled: true, PlayEnabled: true,
		MinAge: 18, GameIDs: gamesOn,
	}); !errors.Is(err, ErrInvalidEntries) {
		t.Fatalf("a state was switched on without official rules: %v", err)
	}
	if _, err := svc.SaveJurisdiction(f.ctx, f.admin, JurisdictionInput{
		CountryID: f.country.String(), StateID: &northID, EarningEnabled: true, PlayEnabled: true,
		MinAge: 18, OfficialRules: &rules, GameIDs: gamesOn,
	}); err != nil {
		t.Fatal(err)
	}

	round := f.liveRound(roundOpts{start: 1000, cost: 1})
	if _, err := f.pool.Exec(f.ctx, `insert into competition.competition_states (competition_id, state_id) values ($1, $2)`, round.ID, north); err != nil {
		t.Fatal(err)
	}

	visible := func(customer uuid.UUID) bool {
		views, err := f.svc.ListCompetitions(f.ctx, SocialActor{CustomerID: customer.String()})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range views {
			if v.ID == round.ID {
				return true
			}
		}
		return false
	}
	if !visible(northerner) {
		t.Fatalf("the round is hidden from a customer in its state")
	}
	if visible(southerner) {
		t.Fatalf("the round is shown to a customer in another state")
	}
	if _, err := f.svc.GetCompetition(f.ctx, round.ID, SocialActor{CustomerID: southerner.String()}); !errors.Is(err, ErrCompetitionNotFound) {
		t.Fatalf("another state can open the round: %v", err)
	}
	if _, err := f.svc.GetCompetition(f.ctx, round.ID, SocialActor{}); !errors.Is(err, ErrCompetitionNotFound) {
		t.Fatalf("a guest can open a state round: %v", err)
	}
	if _, err := f.play(round, northerner, uuid.NewString()); err != nil {
		t.Fatalf("the customer in the state could not play: %v", err)
	}
	if _, err := f.play(round, southerner, uuid.NewString()); err == nil {
		t.Fatalf("a customer in another state played")
	}
	if got := f.balance(southerner); got != 50 {
		t.Fatalf("a refused play took %d entries", 50-got)
	}
	if got := f.balance(northerner); got != 49 {
		t.Fatalf("a play cost %d entries, want 1", 50-got)
	}

	// The global emergency stop ends new plays at once, everywhere.
	if _, err := svc.StopProgram(f.ctx, f.admin, "incident drill"); err != nil {
		t.Fatal(err)
	}
	_, err = f.play(round, northerner, uuid.NewString())
	if _, lifted := svc.SetProgram(f.ctx, f.admin, true, "drill over"); lifted != nil {
		t.Fatal(lifted)
	}
	if err == nil {
		t.Fatalf("a play started during the global stop")
	}
}
