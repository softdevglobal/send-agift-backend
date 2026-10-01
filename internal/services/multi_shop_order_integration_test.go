package services

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/database"
	"myapp/internal/repository"
)

// Integration tests for orders that mix shops and sellers: each shop is priced
// from its delivery zones, and shipping one shop's parcel never touches
// another shop's products.
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
//	    go test ./internal/services -run MultiShop -v
//
// Every row it creates is deleted when the test ends.

type multiShopFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	orders   *OrderService
	shipping *ShippingService
	country  uuid.UUID
	customer uuid.UUID
	sellers  []uuid.UUID
	// Colombo; every shop sits a few km away so seller delivery is in range.
	recipient uuid.UUID
}

type testShop struct {
	seller  uuid.UUID
	shop    uuid.UUID
	product uuid.UUID
	price   int
}

func newMultiShopFixture(t *testing.T) *multiShopFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed multi-shop order tests")
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

	f := &multiShopFixture{t: t, ctx: ctx, pool: pool}
	// Reuse an existing country: iso_code is two letters, so a throwaway one
	// could collide with a real country.
	if err := pool.QueryRow(ctx, `select id from core.countries order by created_at limit 1`).Scan(&f.country); err != nil {
		t.Skipf("no country to attach test rows to: %v", err)
	}

	f.scan(&f.customer, `insert into customer.customers (country_id, email, password_hash, display_name)
		values ($1, $2, 'x', 'Multi Shop Buyer') returning id`, f.country, uuid.NewString()+"@example.test")
	f.scan(&f.recipient, `insert into customer.recipients (customer_id, name, email, phone)
		values ($1, 'Test Recipient', 'recipient@example.test', '+1 555 0100') returning id`, f.customer)
	var addr uuid.UUID
	f.scan(&addr, `insert into customer.recipient_addresses
		(recipient_id, country_id, line1, city, postal_code, latitude, longitude, is_default)
		values ($1, $2, '1 Galle Road', 'Colombo', '00300', 6.9271, 79.8612, true) returning id`, f.recipient, f.country)
	f.exec(`update customer.recipients set default_address_id = $2 where id = $1`, f.recipient, addr)

	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `delete from marketplace.orders where customer_id = $1`, f.customer)
		_, _ = pool.Exec(c, `delete from customer.customers where id = $1`, f.customer)
		if len(f.sellers) > 0 {
			_, _ = pool.Exec(c, `delete from seller.sellers where id = any($1)`, f.sellers)
		}
	})

	shipments := repository.NewShipmentRepository(pool)
	orderRepo := repository.NewOrderRepository(pool)
	f.orders = NewOrderService(orderRepo, repository.NewCustomerRepository(pool), repository.NewCountryRepository(pool), shipments)
	f.shipping = NewShippingService(shipments, orderRepo)
	return f
}

func (f *multiShopFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("exec: %v\n%s", err, sql)
	}
}

func (f *multiShopFixture) scan(dest any, sql string, args ...any) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(dest); err != nil {
		f.t.Fatalf("scan: %v\n%s", err, sql)
	}
}

// seller creates a seller account.
func (f *multiShopFixture) seller() uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	f.scan(&id, `insert into seller.sellers (country_id, legal_name, email, password_hash)
		values ($1, 'Test Seller', $2, 'x') returning id`, f.country, uuid.NewString()+"@seller.test")
	f.sellers = append(f.sellers, id)
	return id
}

// shop creates a shop for seller with one published product, a dispatch
// address near the recipient, and delivery zones: 5 km $50 (1 day), 20 km $80.
func (f *multiShopFixture) shop(seller uuid.UUID, price int) testShop {
	f.t.Helper()
	s := testShop{seller: seller, price: price}
	var addr uuid.UUID
	f.scan(&addr, `insert into seller.seller_addresses (seller_id, country_id, line1, city, latitude, longitude)
		values ($1, $2, '9 Duplication Road', 'Colombo', 6.9000, 79.8550) returning id`, seller, f.country)
	slug := "shop-" + uuid.NewString()[:8]
	f.scan(&s.shop, `insert into seller.shops (seller_id, country_id, name, slug, address_id, latitude, longitude, status)
		values ($1, $2, $3, $3, $4, 6.9000, 79.8550, 'active') returning id`, seller, f.country, slug, addr)
	f.exec(`insert into seller.shop_delivery_zones (shop_id, max_km, price_amount, currency, estimated_days)
		values ($1, 5, 5000, 'USD', 1), ($1, 20, 8000, 'USD', 2)`, s.shop)
	f.scan(&s.product, `insert into seller.products
		(shop_id, name, slug, price_amount, currency, status, parcel_length, parcel_width, parcel_height,
		 parcel_distance_unit, parcel_weight, parcel_mass_unit)
		values ($1, 'Gift', 'gift', $2, 'USD', 'published', '20', '15', '10', 'cm', '1.000', 'kg') returning id`,
		s.shop, price)
	return s
}

func (f *multiShopFixture) zoneQuote(s testShop) OrderShippingQuoteInput {
	return OrderShippingQuoteInput{
		ShopID: s.shop.String(), Mode: SellerDeliveryModeName, Amount: 1, Currency: "USD",
	}
}

func (f *multiShopFixture) placeOrder(quotes []OrderShippingQuoteInput, clientDelivery *int, lines ...testShop) (*orderResult, error) {
	f.t.Helper()
	rid := f.recipient.String()
	in := OrderCreateInput{
		RecipientID:    &rid,
		CountryID:      f.country.String(),
		DeliveryDate:   time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02"),
		DeliveryAmount: clientDelivery,
		ShippingQuotes: quotes,
	}
	for _, l := range lines {
		in.Items = append(in.Items, OrderItemInput{ProductID: l.product.String(), Quantity: 1})
	}
	out, err := f.orders.Create(f.ctx, f.customer.String(), in)
	if err != nil {
		return nil, err
	}
	res := &orderResult{id: out.ID, delivery: out.DeliveryAmount, total: out.TotalAmount, subtotal: out.SubtotalAmount,
		itemsByShop: map[uuid.UUID][]uuid.UUID{}, deliveries: map[uuid.UUID]int{}}
	for _, it := range out.Items {
		res.itemsByShop[it.ShopID] = append(res.itemsByShop[it.ShopID], it.ID)
	}
	for _, d := range out.ShopDeliveries {
		res.deliveries[d.ShopID] = d.Amount
	}
	return res, nil
}

type orderResult struct {
	id                        uuid.UUID
	delivery, total, subtotal int
	itemsByShop               map[uuid.UUID][]uuid.UUID
	deliveries                map[uuid.UUID]int
}

func (f *multiShopFixture) acceptAll(seller uuid.UUID, items []uuid.UUID) {
	f.t.Helper()
	for _, id := range items {
		if _, err := f.orders.AcceptItemForSeller(f.ctx, seller.String(), id.String()); err != nil {
			f.t.Fatalf("accept %s: %v", id, err)
		}
	}
}

func (f *multiShopFixture) statuses(items []uuid.UUID) []string {
	f.t.Helper()
	out := make([]string, 0, len(items))
	for _, id := range items {
		var s string
		f.scan(&s, `select fulfilment_status from marketplace.order_items where id = $1`, id)
		out = append(out, s)
	}
	return out
}

func allEqual(values []string, want string) bool {
	for _, v := range values {
		if v != want {
			return false
		}
	}
	return len(values) > 0
}

func TestMultiShopDeliveryIsSumOfShops(t *testing.T) {
	f := newMultiShopFixture(t)
	a := f.shop(f.seller(), 10000)
	b := f.shop(f.seller(), 2500)

	tamper := 1 // the server must not trust this
	res, err := f.placeOrder([]OrderShippingQuoteInput{f.zoneQuote(a), f.zoneQuote(b)}, &tamper, a, b)
	if err != nil {
		t.Fatal(err)
	}
	// Both shops are inside the 5 km zone ($50). Client amounts are ignored.
	if res.delivery != 5000+5000 {
		t.Fatalf("delivery = %d, want 10000 (sum of zone prices, client amount ignored)", res.delivery)
	}
	if res.total != res.subtotal+res.delivery {
		t.Fatalf("total %d != subtotal %d + delivery %d", res.total, res.subtotal, res.delivery)
	}
	if res.deliveries[a.shop] != 5000 || res.deliveries[b.shop] != 5000 {
		t.Fatalf("shop deliveries = %v", res.deliveries)
	}

	// Each seller only sees their own shop's delivery.
	itemA, err := f.orders.GetItemForSeller(f.ctx, a.seller.String(), res.itemsByShop[a.shop][0].String())
	if err != nil {
		t.Fatal(err)
	}
	if itemA.ShopDelivery == nil || itemA.ShopDelivery.Amount != 5000 || itemA.ShopDelivery.Provider != "Seller delivery" {
		t.Fatalf("seller A shop delivery = %+v", itemA.ShopDelivery)
	}
	if _, err := f.orders.GetItemForSeller(f.ctx, a.seller.String(), res.itemsByShop[b.shop][0].String()); !errors.Is(err, ErrOrderItemNotFound) {
		t.Fatalf("seller A reading seller B's item: err = %v, want not found", err)
	}
}

func TestMultiShopPartialQuoteKeepsPricedShops(t *testing.T) {
	f := newMultiShopFixture(t)
	a := f.shop(f.seller(), 10000)
	b := f.shop(f.seller(), 2500)
	c := f.shop(f.seller(), 700)

	// Every shop on the order is priced from its zone, even when the client
	// only names some of them. All three shops sit inside the $50 zone.
	res, err := f.placeOrder([]OrderShippingQuoteInput{f.zoneQuote(a), f.zoneQuote(b)}, nil, a, b, c)
	if err != nil {
		t.Fatal(err)
	}
	if res.delivery != 15000 {
		t.Fatalf("delivery = %d, want 15000", res.delivery)
	}
	if res.deliveries[a.shop] != 5000 || res.deliveries[b.shop] != 5000 || res.deliveries[c.shop] != 5000 {
		t.Fatalf("shop deliveries = %v", res.deliveries)
	}
}

func TestMultiShopSellerDeliveryIsRepricedByServer(t *testing.T) {
	f := newMultiShopFixture(t)
	a := f.shop(f.seller(), 10000)
	b := f.shop(f.seller(), 2500)

	// Client claims shop delivery is free; the zone price wins for every shop.
	res, err := f.placeOrder([]OrderShippingQuoteInput{
		{ShopID: a.shop.String(), Mode: SellerDeliveryModeName, Amount: 0, Currency: "USD"},
		{ShopID: b.shop.String(), Mode: SellerDeliveryModeName, Amount: 1200, Currency: "USD"},
	}, nil, a, b)
	if err != nil {
		t.Fatal(err)
	}
	// Each shop is ~3.5 km from the recipient → 5 km zone, $50.
	if res.deliveries[a.shop] != 5000 || res.deliveries[b.shop] != 5000 {
		t.Fatalf("seller delivery amounts = %v, want 5000 from the zone", res.deliveries)
	}
	if res.delivery != 10000 {
		t.Fatalf("delivery = %d, want 10000", res.delivery)
	}
	item, err := f.orders.GetItemForSeller(f.ctx, a.seller.String(), res.itemsByShop[a.shop][0].String())
	if err != nil {
		t.Fatal(err)
	}
	if item.ShopDelivery == nil || item.ShopDelivery.Mode != SellerDeliveryModeName || item.ShopDelivery.EstimatedDays == nil || *item.ShopDelivery.EstimatedDays != 1 {
		t.Fatalf("seller delivery row = %+v", item.ShopDelivery)
	}
}

func TestMultiShopRejectsBadQuotes(t *testing.T) {
	f := newMultiShopFixture(t)
	a := f.shop(f.seller(), 10000)
	outsider := f.shop(f.seller(), 999)

	courier := f.zoneQuote(a)
	courier.Mode = "courier"
	if _, err := f.placeOrder([]OrderShippingQuoteInput{courier}, nil, a); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("courier quote: err = %v, want invalid order", err)
	}

	// A quote for a shop with nothing on the order is dropped, not charged.
	res, err := f.placeOrder([]OrderShippingQuoteInput{f.zoneQuote(a), f.zoneQuote(outsider)}, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	if res.delivery != 5000 || len(res.deliveries) != 1 {
		t.Fatalf("delivery = %d rows=%v, want 5000 from shop A only", res.delivery, res.deliveries)
	}
}

func TestMultiShopSameSellerTwoShopsShipSeparately(t *testing.T) {
	f := newMultiShopFixture(t)
	seller := f.seller()
	a := f.shop(seller, 10000)
	b := f.shop(seller, 2500)
	other := f.shop(f.seller(), 700)

	// Two products in shop A, one in shop B, one in another seller's shop.
	res, err := f.placeOrder([]OrderShippingQuoteInput{
		f.zoneQuote(a), f.zoneQuote(b), f.zoneQuote(other),
	}, nil, a, a, b, other)
	if err != nil {
		t.Fatal(err)
	}
	shopA, shopB, shopOther := res.itemsByShop[a.shop], res.itemsByShop[b.shop], res.itemsByShop[other.shop]
	if len(shopA) != 2 || len(shopB) != 1 || len(shopOther) != 1 {
		t.Fatalf("items by shop = %v", res.itemsByShop)
	}

	// The seller list names each line's shop so the screen can split them.
	list, err := f.orders.ListItemsForSeller(f.ctx, seller.String())
	if err != nil {
		t.Fatal(err)
	}
	names := map[uuid.UUID]string{}
	for _, it := range list {
		if it.OrderID == res.id {
			names[it.ShopID] = it.ShopName
		}
	}
	if len(names) != 2 || names[a.shop] == "" || names[b.shop] == "" || names[a.shop] == names[b.shop] {
		t.Fatalf("seller list shop names = %v", names)
	}

	// Shop B still pending must not block shop A.
	f.acceptAll(seller, shopA)

	// Resolving the parcel by order + shop picks shop A's line, never shop B's.
	resolved, err := f.shipping.ResolveShopParcelItem(f.ctx, seller.String(), res.id.String(), a.shop.String(), "accepted")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != shopA[0].String() && resolved != shopA[1].String() {
		t.Fatalf("resolved %s is not a shop A line", resolved)
	}
	// Another seller cannot address this seller's parcel.
	if _, err := f.shipping.ResolveShopParcelItem(f.ctx, other.seller.String(), res.id.String(), a.shop.String()); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("other seller resolving shop A: err = %v, want not found", err)
	}

	if _, err := f.shipping.StartLocalDelivery(f.ctx, seller.String(), resolved, LocalDeliveryInput{}); err != nil {
		t.Fatal(err)
	}
	if st := f.statuses(shopA); !allEqual(st, "dispatched") {
		t.Fatalf("shop A after local delivery = %v, want dispatched", st)
	}
	if st := f.statuses(shopB); !allEqual(st, "pending") {
		t.Fatalf("shop B after shop A's delivery = %v, want still pending", st)
	}
	if st := f.statuses(shopOther); !allEqual(st, "pending") {
		t.Fatalf("other seller after shop A's delivery = %v, want still pending", st)
	}

	// Shop B: a pending line blocks its own parcel only.
	if _, err := f.shipping.StartLocalDelivery(f.ctx, seller.String(), shopB[0].String(), LocalDeliveryInput{}); !errors.Is(err, ErrShippingNotReady) {
		t.Fatalf("shop B delivery while pending: err = %v, want not ready", err)
	}

	if _, err := f.shipping.CompleteLocalDelivery(f.ctx, seller.String(), shopA[0].String()); err != nil {
		t.Fatal(err)
	}
	if st := f.statuses(shopA); !allEqual(st, "delivered") {
		t.Fatalf("shop A after hand-over = %v, want delivered", st)
	}
	var orderStatus string
	f.scan(&orderStatus, `select status from marketplace.orders where id = $1`, res.id)
	if orderStatus == "delivered" {
		t.Fatal("order marked delivered while other shops are still open")
	}

	// Ship the rest; only then is the order delivered.
	f.acceptAll(seller, shopB)
	if _, err := f.shipping.StartLocalDelivery(f.ctx, seller.String(), shopB[0].String(), LocalDeliveryInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.shipping.CompleteLocalDelivery(f.ctx, seller.String(), shopB[0].String()); err != nil {
		t.Fatal(err)
	}
	f.acceptAll(other.seller, shopOther)
	if _, err := f.shipping.StartLocalDelivery(f.ctx, other.seller.String(), shopOther[0].String(), LocalDeliveryInput{}); err != nil {
		t.Fatal(err)
	}
	if st := f.statuses(shopOther); !allEqual(st, "dispatched") {
		t.Fatalf("other seller after local delivery = %v, want dispatched", st)
	}
	f.scan(&orderStatus, `select status from marketplace.orders where id = $1`, res.id)
	if orderStatus == "delivered" {
		t.Fatal("order delivered while another shop's parcel is still in transit")
	}
}
