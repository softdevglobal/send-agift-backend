package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"myapp/internal/repository"
)

// Integration tests for paying orders through Stripe: an unpaid order is
// invisible to sellers, a confirmed payment releases it exactly once, and the
// settlement to a seller is recorded once. Needs TEST_DATABASE_URL like the
// multi-shop tests; the Stripe calls themselves are not made.

type stripeFixture struct {
	*multiShopFixture
	stripe *repository.StripeRepository
	orders *OrderService
}

func newStripeFixture(t *testing.T) *stripeFixture {
	t.Helper()
	m := newMultiShopFixture(t)
	orderRepo := repository.NewOrderRepository(m.pool)
	orderRepo.RequirePayment(true)
	orders := NewOrderService(orderRepo, repository.NewCustomerRepository(m.pool),
		repository.NewCountryRepository(m.pool), repository.NewShipmentRepository(m.pool))
	orders.RequirePayment(true)

	t.Cleanup(func() {
		// Runs before the fixture's own cleanup: payouts and refunds point at
		// orders and sellers without cascading.
		_, _ = m.pool.Exec(m.ctx, `delete from finance.payouts where seller_id = any($1)`, m.sellers)
		_, _ = m.pool.Exec(m.ctx, `delete from finance.refunds where order_id in
			(select id from marketplace.orders where customer_id = $1)`, m.customer)
	})
	return &stripeFixture{multiShopFixture: m, stripe: repository.NewStripeRepository(m.pool), orders: orders}
}

func (f *stripeFixture) place(s testShop) *orderResult {
	f.t.Helper()
	rid := f.recipient.String()
	out, err := f.orders.Create(f.ctx, f.customer.String(), OrderCreateInput{
		RecipientID: &rid, CountryID: f.country.String(),
		DeliveryDate: "2099-01-01",
		Items:        []OrderItemInput{{ProductID: s.product.String(), Quantity: 1}},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	res := &orderResult{id: out.ID, total: out.TotalAmount, itemsByShop: map[uuid.UUID][]uuid.UUID{}}
	for _, it := range out.Items {
		res.itemsByShop[it.ShopID] = append(res.itemsByShop[it.ShopID], it.ID)
	}
	return res
}

func (f *stripeFixture) orderStatus(id uuid.UUID) string {
	f.t.Helper()
	var s string
	f.scan(&s, `select status from marketplace.orders where id = $1`, id)
	return s
}

func TestStripeUnpaidOrderIsHiddenFromSellerUntilPaid(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 4000)
	res := f.place(shop)
	item := res.itemsByShop[shop.shop][0].String()

	if got := f.orderStatus(res.id); got != "pending_payment" {
		t.Fatalf("new order status = %q, want pending_payment", got)
	}
	// Not listed, not readable, not acceptable.
	list, err := f.orders.ListItemsForSeller(f.ctx, shop.seller.String())
	if err != nil || len(list) != 0 {
		t.Fatalf("seller list = %d items, err %v; want none while unpaid", len(list), err)
	}
	if _, err := f.orders.GetItemForSeller(f.ctx, shop.seller.String(), item); !errors.Is(err, ErrOrderItemNotFound) {
		t.Fatalf("seller read unpaid item: err = %v, want not found", err)
	}
	if _, err := f.orders.AcceptItemForSeller(f.ctx, shop.seller.String(), item); err == nil {
		t.Fatal("seller accepted an unpaid order")
	}

	// A confirmed payment releases it, once.
	session := "cs_test_" + uuid.NewString()
	intent := "pi_test_" + uuid.NewString()
	if err := f.stripe.SavePayment(f.ctx, res.id, f.customer, session, res.total, "USD"); err != nil {
		t.Fatal(err)
	}
	paid, err := f.stripe.MarkCheckoutPaid(f.ctx, session, intent)
	if err != nil || paid == nil || !paid.First || paid.OrderStatus != "paid" || paid.OrderID != res.id {
		t.Fatalf("MarkCheckoutPaid = %+v, %v", paid, err)
	}
	if again, err := f.stripe.MarkCheckoutPaid(f.ctx, session, intent); err != nil || again != nil {
		t.Fatalf("second settle = %+v, %v; want nil (already settled)", again, err)
	}
	list, err = f.orders.ListItemsForSeller(f.ctx, shop.seller.String())
	if err != nil || len(list) != 1 {
		t.Fatalf("seller list after payment = %d, err %v; want 1", len(list), err)
	}
	if _, err := f.orders.AcceptItemForSeller(f.ctx, shop.seller.String(), item); err != nil {
		t.Fatalf("accept after payment: %v", err)
	}
	// The intent replaced the session id, so a refund finds it.
	if id, err := f.stripe.PaidIntentForOrder(f.ctx, res.id); err != nil || id != intent {
		t.Fatalf("PaidIntentForOrder = %q, %v; want %q", id, err, intent)
	}
}

func TestStripeSettlementIsRecordedOnce(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 5000)
	res := f.place(shop)
	item := res.itemsByShop[shop.shop][0]

	session, intent := "cs_test_"+uuid.NewString(), "pi_test_"+uuid.NewString()
	if err := f.stripe.SavePayment(f.ctx, res.id, f.customer, session, res.total, "USD"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stripe.MarkCheckoutPaid(f.ctx, session, intent); err != nil {
		t.Fatal(err)
	}
	f.exec(`update marketplace.order_items set fulfilment_status = 'delivered' where id = $1`, item)

	// No payout account yet: nothing is due.
	if due, err := f.stripe.DueTransfers(f.ctx, 10); err != nil || containsItem(due, item) {
		t.Fatalf("due before account = %v, %v", due, err)
	}
	acct := "acct_test_" + uuid.NewString()[:8]
	f.exec(`insert into seller.seller_payout_accounts
		(seller_id, provider, provider_account_id, country_id, payouts_enabled, charges_enabled, capabilities_status)
		values ($1, 'stripe', $2, $3, true, true, 'enabled')`, shop.seller, acct, f.country)

	due, err := f.stripe.DueTransfers(f.ctx, 50)
	if err != nil || !containsItem(due, item) {
		t.Fatalf("due = %v, %v; want the delivered line", due, err)
	}
	var line repository.DueTransfer
	for _, d := range due {
		if d.OrderItemID == item {
			line = d
		}
	}
	if line.AccountID != acct || line.Amount != 5000 {
		t.Fatalf("due line = %+v", line)
	}
	if err := f.stripe.RecordTransfer(f.ctx, line, "tr_test_"+uuid.NewString(), 500); err != nil {
		t.Fatal(err)
	}
	// Recording twice, or listing again, never pays it again.
	if err := f.stripe.RecordTransfer(f.ctx, line, "tr_test_"+uuid.NewString(), 500); err != nil {
		t.Fatal(err)
	}
	if due, err := f.stripe.DueTransfers(f.ctx, 50); err != nil || containsItem(due, item) {
		t.Fatalf("due after payout = %v, %v", due, err)
	}
	var payouts, splits, net int
	f.scan(&payouts, `select count(*) from finance.payouts where order_item_id = $1`, item)
	f.scan(&net, `select amount from finance.payouts where order_item_id = $1`, item)
	f.scan(&splits, `select count(*) from finance.payment_splits s
		join finance.payments p on p.id = s.payment_id where p.order_id = $1`, res.id)
	if payouts != 1 || net != 4500 || splits != 2 {
		t.Fatalf("payouts=%d net=%d splits=%d; want 1 payout of 4500 with 2 splits", payouts, net, splits)
	}

	// The seller's page shows it: one row, and totals that agree.
	items, totals, err := f.stripe.SellerPayouts(f.ctx, shop.seller.String(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Net != 4500 || items[0].Fee != 500 || items[0].Gross != 5000 ||
		items[0].ProductName != "Gift" || items[0].OrderNumber == "" {
		t.Fatalf("payout rows = %+v", items)
	}
	if len(totals) != 1 || totals[0].PaidOut != 4500 || totals[0].Fees != 500 ||
		totals[0].OnTheWay != 0 || totals[0].Awaiting != 0 {
		t.Fatalf("payout totals = %+v", totals)
	}
}

// What a seller is still owed shows before it is paid: gifts on the way, and
// gifts delivered but not yet settled.
func TestStripePayoutTotalsShowWhatIsStillToCome(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 2000)
	onTheWay, delivered := f.place(shop), f.place(shop)
	for _, o := range []*orderResult{onTheWay, delivered} {
		session, intent := "cs_test_"+uuid.NewString(), "pi_test_"+uuid.NewString()
		if err := f.stripe.SavePayment(f.ctx, o.id, f.customer, session, o.total, "USD"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.stripe.MarkCheckoutPaid(f.ctx, session, intent); err != nil {
			t.Fatal(err)
		}
	}
	f.exec(`update marketplace.order_items set fulfilment_status = 'delivered' where id = $1`,
		delivered.itemsByShop[shop.shop][0])

	_, totals, err := f.stripe.SellerPayouts(f.ctx, shop.seller.String(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 1 || totals[0].OnTheWay != 2000 || totals[0].Awaiting != 2000 || totals[0].PaidOut != 0 {
		t.Fatalf("totals = %+v; want 2000 on the way, 2000 awaiting, nothing paid", totals)
	}
}

// Stripe cannot create accounts in every country (Sri Lanka is one), so the
// country can be overridden; the seller's own is used otherwise.
func TestStripeOnboardingUsesTheConfiguredConnectCountry(t *testing.T) {
	f := newStripeFixture(t)
	seller := f.seller()
	var sellerISO string
	f.scan(&sellerISO, `select c.iso_code from seller.sellers s
		join core.countries c on c.id = s.country_id where s.id = $1`, seller)

	var countries []string
	stripe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/accounts":
			countries = append(countries, r.PostForm.Get("country"))
			_, _ = w.Write([]byte(`{"id":"acct_` + uuid.NewString()[:8] + `"}`))
		case "/account_links":
			_, _ = w.Write([]byte(`{"url":"https://connect.stripe.com/setup/x"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer stripe.Close()
	client := NewStripeClient("sk_test_x")
	client.base = stripe.URL

	svc := NewStripeService(f.stripe, client, "", "", 0, "http://app")
	if _, err := svc.ConnectOnboarding(f.ctx, seller.String()); err != nil {
		t.Fatal(err)
	}
	if len(countries) != 1 || countries[0] != strings.ToUpper(sellerISO) {
		t.Fatalf("account created in %v, want the seller's own %q", countries, sellerISO)
	}

	other := f.seller()
	svc.UseConnectCountry("au")
	if _, err := svc.ConnectOnboarding(f.ctx, other.String()); err != nil {
		t.Fatal(err)
	}
	if len(countries) != 2 || countries[1] != "AU" {
		t.Fatalf("account created in %v, want AU for the second", countries)
	}
}

// An order follows its items: the customer's timeline moves as the seller
// accepts and dispatches, a parcel on its way can no longer be cancelled, and
// the seller's panel can still tell it is a local delivery after a reload.
func TestOrderStatusFollowsItemsAndSellerSeesTheShipment(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 3000)
	res := f.place(shop)
	item := res.itemsByShop[shop.shop][0].String()

	session, intent := "cs_test_"+uuid.NewString(), "pi_test_"+uuid.NewString()
	if err := f.stripe.SavePayment(f.ctx, res.id, f.customer, session, res.total, "USD"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stripe.MarkCheckoutPaid(f.ctx, session, intent); err != nil {
		t.Fatal(err)
	}
	if got := f.orderStatus(res.id); got != "paid" {
		t.Fatalf("after payment = %q, want paid", got)
	}

	if _, err := f.orders.AcceptItemForSeller(f.ctx, shop.seller.String(), item); err != nil {
		t.Fatal(err)
	}
	if got := f.orderStatus(res.id); got != "accepted" {
		t.Fatalf("after accept = %q, want accepted", got)
	}

	// Hand the parcel to the shop's own delivery.
	shipping := NewShippingService(repository.NewShipmentRepository(f.pool), repository.NewOrderRepository(f.pool))
	if _, err := shipping.StartLocalDelivery(f.ctx, shop.seller.String(), item, LocalDeliveryInput{}); err != nil {
		t.Fatalf("start local delivery: %v", err)
	}
	if got := f.orderStatus(res.id); got != "dispatched" {
		t.Fatalf("after dispatch = %q, want dispatched", got)
	}

	// The seller reloads: the shipment is there, so "Mark as delivered" shows.
	details, err := f.orders.GetItemForSeller(f.ctx, shop.seller.String(), item)
	if err != nil {
		t.Fatal(err)
	}
	if details.Tracking == nil || details.Tracking.DeliveryMode != "seller_managed" ||
		details.Tracking.CourierProvider == nil || *details.Tracking.CourierProvider != "Local delivery" ||
		details.Tracking.Status != "in_transit" {
		t.Fatalf("seller item tracking = %+v", details.Tracking)
	}

	// A parcel on its way is no longer cancellable.
	if _, err := f.orders.Cancel(f.ctx, f.customer.String(), res.id.String()); !errors.Is(err, ErrOrderNotCancellable) {
		t.Fatalf("cancel a dispatched order: err = %v, want not cancellable", err)
	}

	// Marking it delivered completes the order.
	if _, err := shipping.CompleteLocalDelivery(f.ctx, shop.seller.String(), item); err != nil {
		t.Fatal(err)
	}
	if got := f.orderStatus(res.id); got != "delivered" {
		t.Fatalf("after delivery = %q, want delivered", got)
	}
}

func TestStripeSellerAccountSavesAndReadsBack(t *testing.T) {
	f := newStripeFixture(t)
	seller := f.seller()

	st, err := f.stripe.GetSellerStripe(f.ctx, seller.String())
	if err != nil || st.AccountID != "" || st.PayoutsEnabled {
		t.Fatalf("before connecting = %+v, %v", st, err)
	}
	acct := "acct_test_" + uuid.NewString()[:8]
	if err := f.stripe.SetSellerAccount(f.ctx, seller.String(), acct); err != nil {
		t.Fatal(err)
	}
	// Choosing another account replaces it rather than adding a second.
	acct2 := "acct_test_" + uuid.NewString()[:8]
	if err := f.stripe.SetSellerAccount(f.ctx, seller.String(), acct2); err != nil {
		t.Fatal(err)
	}
	if err := f.stripe.UpdateAccountStatus(f.ctx, acct2, repository.AccountStatus{
		ChargesEnabled: true, PayoutsEnabled: true, DetailsSubmitted: true,
	}); err != nil {
		t.Fatal(err)
	}
	st, err = f.stripe.GetSellerStripe(f.ctx, seller.String())
	if err != nil || st.AccountID != acct2 || !st.PayoutsEnabled || !st.DetailsSubmitted || st.CountryISO == "" {
		t.Fatalf("after connecting = %+v, %v", st, err)
	}
}

func TestStripeFullRefundMarksOrderRefundedButNotCancelled(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 3000)

	// A paid, undelivered order that is then refunded in full.
	refunded := f.place(shop)
	s1, p1 := "cs_test_"+uuid.NewString(), "pi_test_"+uuid.NewString()
	_ = f.stripe.SavePayment(f.ctx, refunded.id, f.customer, s1, refunded.total, "USD")
	if _, err := f.stripe.MarkCheckoutPaid(f.ctx, s1, p1); err != nil {
		t.Fatal(err)
	}
	if err := f.stripe.ApplyRefund(f.ctx, p1, refunded.total); err != nil {
		t.Fatal(err)
	}
	if got := f.orderStatus(refunded.id); got != "refunded" {
		t.Fatalf("refunded order status = %q", got)
	}
	// Repeating the same cumulative amount adds no second refund row.
	if err := f.stripe.ApplyRefund(f.ctx, p1, refunded.total); err != nil {
		t.Fatal(err)
	}
	var rows int
	f.scan(&rows, `select count(*) from finance.refunds where order_id = $1`, refunded.id)
	if rows != 1 {
		t.Fatalf("refund rows = %d, want 1", rows)
	}

	// An order cancelled by the customer keeps its "cancelled" status when
	// the refund for it arrives.
	cancelled := f.place(shop)
	s2, p2 := "cs_test_"+uuid.NewString(), "pi_test_"+uuid.NewString()
	_ = f.stripe.SavePayment(f.ctx, cancelled.id, f.customer, s2, cancelled.total, "USD")
	if _, err := f.stripe.MarkCheckoutPaid(f.ctx, s2, p2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.orders.Cancel(f.ctx, f.customer.String(), cancelled.id.String()); err != nil {
		t.Fatal(err)
	}
	if err := f.stripe.ApplyRefund(f.ctx, p2, cancelled.total); err != nil {
		t.Fatal(err)
	}
	if got := f.orderStatus(cancelled.id); got != "cancelled" {
		t.Fatalf("cancelled order status after refund = %q, want cancelled", got)
	}
}

func TestStripePaymentForCancelledOrderIsFlaggedForRefund(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 3000)
	res := f.place(shop)
	session, intent := "cs_test_"+uuid.NewString(), "pi_test_"+uuid.NewString()
	_ = f.stripe.SavePayment(f.ctx, res.id, f.customer, session, res.total, "USD")

	// Cancelled while the payment page was open, then paid anyway.
	if _, err := f.orders.Cancel(f.ctx, f.customer.String(), res.id.String()); err != nil {
		t.Fatal(err)
	}
	paid, err := f.stripe.MarkCheckoutPaid(f.ctx, session, intent)
	if err != nil || paid == nil || paid.OrderStatus != "cancelled" {
		t.Fatalf("MarkCheckoutPaid = %+v, %v; want status cancelled so it is refunded", paid, err)
	}
	if got := f.orderStatus(res.id); got != "cancelled" {
		t.Fatalf("order status = %q, want cancelled", got)
	}
}

func TestStripeAbandonedOrdersExpireButLegacyOrdersAreLeftAlone(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 3000)
	fresh := f.place(shop)
	legacy := f.place(shop)

	var cutoff time.Time
	f.scan(&cutoff, `select applied_at from schema_migrations where version = '000067_stripe_payments'`)
	// "legacy" was created before payments went live; "fresh" just after.
	f.exec(`update marketplace.orders set created_at = $2 where id = $1`, legacy.id, cutoff.Add(-time.Hour))
	f.exec(`update marketplace.orders set created_at = $2 where id = $1`, fresh.id, cutoff.Add(time.Second))

	has := func(list []repository.UnpaidOrder, id uuid.UUID) bool {
		for _, o := range list {
			if o.OrderID == id {
				return true
			}
		}
		return false
	}
	// Anything older than "now" counts as waiting long enough.
	list, err := f.stripe.ExpiredUnpaidOrders(f.ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !has(list, fresh.id) || has(list, legacy.id) {
		t.Fatalf("expired = %v; want the new order only, never the legacy one", list)
	}
	// Not yet old enough for the real threshold.
	list, err = f.stripe.ExpiredUnpaidOrders(f.ctx, 100000, 1000)
	if err != nil || has(list, fresh.id) {
		t.Fatalf("expired at a long threshold = %v, %v; want none", list, err)
	}
}

func TestStripeLegacyUnpaidOrdersStayWithSellers(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 3000)
	res := f.place(shop)

	var cutoff time.Time
	f.scan(&cutoff, `select applied_at from schema_migrations where version = '000067_stripe_payments'`)
	f.exec(`update marketplace.orders set created_at = $2 where id = $1`, res.id, cutoff.Add(-time.Hour))

	list, err := f.orders.ListItemsForSeller(f.ctx, shop.seller.String())
	if err != nil || len(list) != 1 {
		t.Fatalf("seller list = %d, err %v; want the pre-payment order to stay visible", len(list), err)
	}
	if _, err := f.orders.AcceptItemForSeller(f.ctx, shop.seller.String(), res.itemsByShop[shop.shop][0].String()); err != nil {
		t.Fatalf("accept legacy order: %v", err)
	}
}

func TestStripeReceiptListsGiftsAndDeliveryAndAddsUp(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 2500)
	res := f.place(shop)

	svc := NewStripeService(f.stripe, nil, "", "", 0, "http://app")
	order, err := f.stripe.GetPayableOrder(f.ctx, f.customer.String(), res.id.String())
	if err != nil {
		t.Fatal(err)
	}
	lines := svc.receiptLines(f.ctx, order)
	if len(lines) == 0 {
		t.Fatalf("no receipt lines for a %d order", order.Total)
	}
	sum := 0
	for _, l := range lines {
		sum += l.UnitAmount * l.Quantity
	}
	if sum != order.Total {
		t.Fatalf("lines add up to %d, order total is %d", sum, order.Total)
	}
	if lines[0].Name != "Gift" || lines[0].UnitAmount != 2500 {
		t.Fatalf("first line = %+v, want the gift at 2500", lines[0])
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last.Name, "Delivery") {
		t.Fatalf("last line = %+v, want a delivery line", last)
	}
}

// The webhook never arrived, but Stripe says the session is paid: asking for
// the order's payment settles it, once.
func TestStripeSyncSettlesAMissedWebhook(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 2500)
	res := f.place(shop)
	session := "cs_test_" + uuid.NewString()
	intent := "pi_test_" + uuid.NewString()
	if err := f.stripe.SavePayment(f.ctx, res.id, f.customer, session, res.total, "USD"); err != nil {
		t.Fatal(err)
	}

	stripe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/checkout/sessions/"+session {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + session + `","status":"complete","payment_status":"paid","payment_intent":"` + intent + `"}`))
	}))
	defer stripe.Close()
	client := NewStripeClient("sk_test_x")
	client.base = stripe.URL

	svc := NewStripeService(f.stripe, client, "", "", 0, "http://app")
	paidCalls := 0
	svc.OnOrderPaid(func(_ context.Context, id uuid.UUID) {
		if id == res.id {
			paidCalls++
		}
	})

	status, err := svc.SyncOrderPayment(f.ctx, f.customer.String(), res.id.String())
	if err != nil || status != "paid" {
		t.Fatalf("sync = %q, %v; want paid", status, err)
	}
	// Again, and via the background reconcile: nothing is applied twice.
	if _, err := svc.SyncOrderPayment(f.ctx, f.customer.String(), res.id.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReconcilePending(f.ctx, 50); err != nil {
		t.Fatal(err)
	}
	if paidCalls != 1 {
		t.Fatalf("on-paid ran %d times, want exactly once", paidCalls)
	}
	if id, err := f.stripe.PaidIntentForOrder(f.ctx, res.id); err != nil || id != intent {
		t.Fatalf("PaidIntentForOrder = %q, %v; want %q", id, err, intent)
	}
}

func containsItem(due []repository.DueTransfer, item uuid.UUID) bool {
	for _, d := range due {
		if d.OrderItemID == item {
			return true
		}
	}
	return false
}

func TestStripeManualPayoutsAreHeldUntilTheSellerWithdraws(t *testing.T) {
	f := newStripeFixture(t)
	shop := f.shop(f.seller(), 5000)
	res := f.place(shop)
	item := res.itemsByShop[shop.shop][0]

	session, intent := "cs_test_"+uuid.NewString(), "pi_test_"+uuid.NewString()
	if err := f.stripe.SavePayment(f.ctx, res.id, f.customer, session, res.total, "USD"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stripe.MarkCheckoutPaid(f.ctx, session, intent); err != nil {
		t.Fatal(err)
	}
	f.exec(`update marketplace.order_items set fulfilment_status = 'delivered' where id = $1`, item)
	f.exec(`insert into seller.seller_payout_accounts
		(seller_id, provider, provider_account_id, country_id, payouts_enabled, charges_enabled, capabilities_status)
		values ($1, 'stripe', $2, $3, true, true, 'enabled')`,
		shop.seller, "acct_test_"+uuid.NewString()[:8], f.country)

	// Automatic is the default: the loop picks the line up.
	if due, err := f.stripe.DueTransfers(f.ctx, 50); err != nil || !containsItem(due, item) {
		t.Fatalf("automatic due = %v, %v", due, err)
	}
	ok, err := f.stripe.SetPayoutMode(f.ctx, shop.seller.String(), "manual")
	if err != nil || !ok {
		t.Fatalf("set manual = %v, %v", ok, err)
	}
	// Manual: the loop leaves it, the seller's own withdrawal still sees it.
	if due, err := f.stripe.DueTransfers(f.ctx, 50); err != nil || containsItem(due, item) {
		t.Fatalf("manual due = %v, %v; want it held", due, err)
	}
	mine, err := f.stripe.DueTransfersForSeller(f.ctx, shop.seller.String(), 50)
	if err != nil || !containsItem(mine, item) {
		t.Fatalf("withdrawable = %v, %v", mine, err)
	}
	// A seller with no payout account cannot pick a mode.
	if ok, err := f.stripe.SetPayoutMode(f.ctx, uuid.NewString(), "manual"); err != nil || ok {
		t.Fatalf("mode for unknown seller = %v, %v", ok, err)
	}
}
