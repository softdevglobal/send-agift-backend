package services

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// Integration tests for the first free gift program. The one-time limits, the
// budget and the stock are all decided by the database, so the cases that
// matter are the parallel ones, and those need a real Postgres:
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
//	    go test ./internal/services -run FreeGift -v
//
// Every row it creates is deleted when the test ends.

const (
	freeGiftTestPrice    = 10000
	freeGiftTestDelivery = 5000 // the fixture's 5 km zone
	// Only the product comes from the budget; the customer pays delivery.
	freeGiftTestCost = freeGiftTestPrice
)

var freeGiftPhoneSeq atomic.Int64

type freeGiftFixture struct {
	*multiShopFixture
	gifts     *FreeGiftService
	customers []uuid.UUID
}

type freeGiftOffer struct {
	testShop
}

func newFreeGiftFixture(t *testing.T) *freeGiftFixture {
	t.Helper()
	base := newMultiShopFixture(t)
	f := &freeGiftFixture{multiShopFixture: base}
	f.gifts = NewFreeGiftService(
		repository.NewFreeGiftRepository(base.pool), repository.NewCustomerRepository(base.pool),
		repository.NewCountryRepository(base.pool), base.orders,
		true, "integration-test-secret-integration-test", "v1", "+61")
	freeGiftPhoneSeq.CompareAndSwap(0, time.Now().UnixNano()%900000)

	// Registered after the base fixture's cleanup, so it runs first: claims
	// point at orders, and orders at customers.
	t.Cleanup(func() {
		ids := append([]uuid.UUID{base.customer}, f.customers...)
		for _, sql := range []string{
			`delete from promo.free_gift_claims where customer_id = any($1)
			    or order_id in (select id from marketplace.orders where customer_id = any($1))`,
			`delete from marketplace.orders where customer_id = any($1)`,
			`delete from customer.customers where id = any($1)`,
		} {
			if _, err := base.pool.Exec(base.ctx, sql, ids); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
	})
	return f
}

// phone is a fresh, valid Australian mobile in E.164.
func (f *freeGiftFixture) phone() string {
	return fmt.Sprintf("+61412%06d", freeGiftPhoneSeq.Add(1)%1000000)
}

// sender creates a customer with a verified email and phone.
func (f *freeGiftFixture) sender() uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	phone := f.phone()
	f.scan(&id, `insert into customer.customers
		(country_id, email, password_hash, display_name, phone, phone_e164, phone_verified_at, email_verified_at)
		values ($1, $2, 'x', 'Free Gift Sender', $3, $3, now(), now()) returning id`,
		f.country, uuid.NewString()+"@example.test", phone)
	f.customers = append(f.customers, id)
	return id
}

// savedRecipient saves a recipient for customer, a few km from every test shop.
func (f *freeGiftFixture) savedRecipient(customer uuid.UUID, email, phone string) uuid.UUID {
	f.t.Helper()
	var id, addr uuid.UUID
	f.scan(&id, `insert into customer.recipients (customer_id, name, email, phone)
		values ($1, 'Free Gift Recipient', nullif($2, ''), nullif($3, '')) returning id`, customer, email, phone)
	f.scan(&addr, `insert into customer.recipient_addresses
		(recipient_id, country_id, line1, city, postal_code, latitude, longitude, is_default)
		values ($1, $2, '1 Galle Road', 'Colombo', '00300', 6.9271, 79.8612, true) returning id`, id, f.country)
	f.exec(`update customer.recipients set default_address_id = $2 where id = $1`, id, addr)
	return id
}

// newRecipient is a recipient nobody has sent a free gift to.
func (f *freeGiftFixture) newRecipient(customer uuid.UUID) uuid.UUID {
	return f.savedRecipient(customer, uuid.NewString()+"@recipient.test", f.phone())
}

// offer creates a seller with an active program of this budget and one
// product in it, with this much stock.
func (f *freeGiftFixture) offer(budget int64, stock int) freeGiftOffer {
	f.t.Helper()
	o := freeGiftOffer{testShop: f.shop(f.seller(), freeGiftTestPrice)}
	f.exec(`insert into seller.inventory (product_id, available_qty) values ($1, $2)`, o.product, stock)
	f.exec(`update seller.sellers set free_gift_terms_accepted_at = now() where id = $1`, o.seller)
	if _, err := f.gifts.AdminSetProgram(f.ctx, o.seller, AdminProgramInput{
		BudgetCap: budget, Currency: "USD", Status: "active",
	}); err != nil {
		f.t.Fatalf("set program: %v", err)
	}
	if _, err := f.gifts.SetProduct(f.ctx, o.seller.String(), o.product, FreeGiftProductInput{}); err != nil {
		f.t.Fatalf("set product: %v", err)
	}
	return o
}

func (f *freeGiftFixture) send(customer, recipient uuid.UUID, o freeGiftOffer, key string) (*models.OrderDetails, error) {
	return f.gifts.Send(f.ctx, customer.String(), key, FreeGiftSendInput{
		ProductID:    o.product.String(),
		RecipientID:  recipient.String(),
		CountryID:    f.country.String(),
		DeliveryDate: time.Now().UTC().AddDate(0, 0, 3).Format("2006-01-02"),
	})
}

func (f *freeGiftFixture) int(sql string, args ...any) int64 {
	f.t.Helper()
	var n int64
	f.scan(&n, sql, args...)
	return n
}

func (f *freeGiftFixture) budget(o freeGiftOffer) (held, spent int64) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.ctx, `
		select budget_held, budget_spent from promo.free_gift_programs where seller_id = $1`, o.seller).
		Scan(&held, &spent); err != nil {
		f.t.Fatal(err)
	}
	return held, spent
}

func (f *freeGiftFixture) stock(o freeGiftOffer) (available, reserved int64) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.ctx, `
		select available_qty, reserved_qty from seller.inventory where product_id = $1`, o.product).
		Scan(&available, &reserved); err != nil {
		f.t.Fatal(err)
	}
	return available, reserved
}

func (f *freeGiftFixture) claims(orderID uuid.UUID, role, state string) int64 {
	return f.int(`select count(*) from promo.free_gift_claims where order_id = $1 and role = $2 and state = $3`,
		orderID, role, state)
}

func TestFreeGiftFirstSend(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(100000, 5)
	sender := f.sender()
	rec := f.newRecipient(sender)

	order, err := f.send(sender, rec, o, "first-send-key")
	if err != nil {
		t.Fatal(err)
	}
	if order.TotalAmount != freeGiftTestDelivery || order.Status != "pending_payment" || order.OrderKind != models.OrderKindFreeGift {
		t.Fatalf("order = total %d status %s kind %s, want %d / pending_payment / free_gift", order.TotalAmount, order.Status, order.OrderKind, freeGiftTestDelivery)
	}
	if order.SubtotalAmount != freeGiftTestPrice || order.DeliveryAmount != freeGiftTestDelivery || order.FreeGiftCost != freeGiftTestCost {
		t.Fatalf("subtotal %d delivery %d cost %d", order.SubtotalAmount, order.DeliveryAmount, order.FreeGiftCost)
	}
	if len(order.Items) != 1 || order.Items[0].Quantity != 1 || order.Items[0].UnitAmount != freeGiftTestPrice || order.Items[0].RewardPoints != 0 {
		t.Fatalf("items = %+v", order.Items)
	}
	if send, recv := f.claims(order.ID, "send", "used"), f.claims(order.ID, "receive", "used"); send != 3 || recv != 2 {
		t.Fatalf("claims = %d send, %d receive; want 3 and 2", send, recv)
	}
	if held, spent := f.budget(o); held != freeGiftTestCost || spent != 0 {
		t.Fatalf("budget held %d spent %d, want %d and 0", held, spent, freeGiftTestCost)
	}
	if available, reserved := f.stock(o); available != 5 || reserved != 1 {
		t.Fatalf("stock available %d reserved %d, want 5 and 1", available, reserved)
	}

	// What the customer reads back says the same.
	got, err := f.orders.Get(f.ctx, sender.String(), order.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if got.OrderKind != models.OrderKindFreeGift || got.FreeGiftCost != freeGiftTestCost || got.TotalAmount != freeGiftTestDelivery {
		t.Fatalf("read back = kind %s cost %d total %d", got.OrderKind, got.FreeGiftCost, got.TotalAmount)
	}
	// And the seller sees a free gift they fund.
	item, err := f.orders.GetItemForSeller(f.ctx, o.seller.String(), order.Items[0].ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if item.Order.OrderKind != models.OrderKindFreeGift || item.Order.FreeGiftCost != freeGiftTestCost {
		t.Fatalf("seller view = %+v", item.Order)
	}

	// The same key is the same order, however often it is retried.
	again, err := f.send(sender, rec, o, "first-send-key")
	if err != nil || again.ID != order.ID {
		t.Fatalf("retry = %v, %v; want the first order", again, err)
	}
	// The same key for something else is refused.
	if _, err := f.send(sender, f.newRecipient(sender), o, "first-send-key"); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("same key, other recipient: err = %v, want mismatch", err)
	}
	// A second gift, to anyone, is refused.
	if _, err := f.send(sender, f.newRecipient(sender), o, "second-send-key"); !errors.Is(err, ErrFreeGiftAlreadyUsed) {
		t.Fatalf("second send: err = %v, want already used", err)
	}

	st, err := f.gifts.Status(f.ctx, sender.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Used || st.Available {
		t.Fatalf("status after sending = %+v", st)
	}
}

func TestFreeGiftNeedsVerifiedEmailAndPhone(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(100000, 5)

	noPhone := f.sender()
	f.exec(`update customer.customers set phone_verified_at = null where id = $1`, noPhone)
	if _, err := f.send(noPhone, f.newRecipient(noPhone), o, "no-phone-key"); !errors.Is(err, ErrVerifyPhoneRequired) {
		t.Fatalf("unverified phone: err = %v", err)
	}
	st, err := f.gifts.Status(f.ctx, noPhone.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Available || st.Used || len(st.Missing) != 1 || st.Missing[0] != "phone" {
		t.Fatalf("status = %+v, want missing phone", st)
	}

	noEmail := f.sender()
	f.exec(`update customer.customers set email_verified_at = null where id = $1`, noEmail)
	if _, err := f.send(noEmail, f.newRecipient(noEmail), o, "no-email-key"); !errors.Is(err, ErrVerifyEmailRequired) {
		t.Fatalf("unverified email: err = %v", err)
	}

	locked := f.sender()
	f.exec(`update customer.customers set redemption_locked_until = now() + interval '1 day' where id = $1`, locked)
	if _, err := f.send(locked, f.newRecipient(locked), o, "locked-key-1"); !errors.Is(err, ErrRedemptionLocked) {
		t.Fatalf("email-change hold: err = %v", err)
	}
	if held, _ := f.budget(o); held != 0 {
		t.Fatalf("budget held %d after refusals, want 0", held)
	}
}

func TestFreeGiftRecipientRules(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(100000, 5)
	sender := f.sender()

	noEmail := f.savedRecipient(sender, "", f.phone())
	if _, err := f.send(sender, noEmail, o, "rec-no-email"); !errors.Is(err, ErrFreeGiftRecipientContact) {
		t.Fatalf("recipient without email: err = %v", err)
	}
	badPhone := f.savedRecipient(sender, uuid.NewString()+"@recipient.test", "call me")
	if _, err := f.send(sender, badPhone, o, "rec-bad-phone"); !errors.Is(err, ErrFreeGiftRecipientContact) {
		t.Fatalf("recipient with unreadable phone: err = %v", err)
	}
	st, err := f.gifts.Status(f.ctx, sender.String(), badPhone.String())
	if err != nil {
		t.Fatal(err)
	}
	if st.Recipient == nil || st.Recipient.CanReceive || st.Recipient.Reason == nil || *st.Recipient.Reason != "RECIPIENT_CONTACT_REQUIRED" {
		t.Fatalf("status recipient = %+v", st.Recipient)
	}

	// The sender's own phone under another email is still the sender.
	var ownPhone string
	f.scan(&ownPhone, `select phone_e164 from customer.customers where id = $1`, sender)
	self := f.savedRecipient(sender, uuid.NewString()+"@recipient.test", ownPhone)
	if _, err := f.send(sender, self, o, "rec-self-key"); !errors.Is(err, ErrFreeGiftSelf) {
		t.Fatalf("self gift: err = %v", err)
	}

	// Another customer's recipient is not this customer's to send to.
	other := f.sender()
	if _, err := f.send(sender, f.newRecipient(other), o, "rec-not-mine"); !errors.Is(err, ErrRecipientNotFound) {
		t.Fatalf("someone else's recipient: err = %v", err)
	}
}

// Two tabs, twenty requests, twenty different keys: one order.
func TestFreeGiftParallelSendsFromOneCustomer(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(1000000, 50)
	sender := f.sender()
	recipients := make([]uuid.UUID, 20)
	for i := range recipients {
		recipients[i] = f.newRecipient(sender)
	}

	var ok, used atomic.Int32
	var wg sync.WaitGroup
	for i := range recipients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.send(sender, recipients[i], o, fmt.Sprintf("parallel-key-%02d", i))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrFreeGiftAlreadyUsed):
				used.Add(1)
			default:
				t.Errorf("request %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || used.Load() != 19 {
		t.Fatalf("%d orders, %d already used; want 1 and 19", ok.Load(), used.Load())
	}
	if n := f.int(`select count(*) from marketplace.orders where customer_id = $1`, sender); n != 1 {
		t.Fatalf("%d orders written, want 1", n)
	}
	if held, _ := f.budget(o); held != freeGiftTestCost {
		t.Fatalf("budget held %d, want one gift (%d)", held, freeGiftTestCost)
	}
	if _, reserved := f.stock(o); reserved != 1 {
		t.Fatalf("reserved %d, want 1", reserved)
	}
}

// A double click sends the same key twice at once: both get the one order.
func TestFreeGiftDoubleClick(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(100000, 5)
	sender := f.sender()
	rec := f.newRecipient(sender)

	ids := make([]uuid.UUID, 6)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, err := f.send(sender, rec, o, "double-click-key")
			if err != nil {
				t.Errorf("click %d: %v", i, err)
				return
			}
			ids[i] = order.ID
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] || id == uuid.Nil {
			t.Fatalf("orders = %v, want the same one each time", ids)
		}
	}
}

// The limit is on the recipient's email and phone, not on the recipient row:
// two customers who each saved the same person race for them.
func TestFreeGiftOneRecipientManySenders(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(1000000, 50)
	email, phone := uuid.NewString()+"@recipient.test", f.phone()

	const n = 8
	senders := make([]uuid.UUID, n)
	recipients := make([]uuid.UUID, n)
	for i := range senders {
		senders[i] = f.sender()
		recipients[i] = f.savedRecipient(senders[i], email, phone)
	}
	var ok, refused atomic.Int32
	var wg sync.WaitGroup
	for i := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.send(senders[i], recipients[i], o, fmt.Sprintf("one-recipient-%02d", i))
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrFreeGiftRecipientUsed):
				refused.Add(1)
			default:
				t.Errorf("sender %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || refused.Load() != n-1 {
		t.Fatalf("%d orders, %d refused; want 1 and %d", ok.Load(), refused.Load(), n-1)
	}

	// The same phone under a new email is still the same recipient, and the
	// losers kept their own free gift.
	late := f.sender()
	if _, err := f.send(late, f.savedRecipient(late, uuid.NewString()+"@recipient.test", phone), o, "one-recipient-late"); !errors.Is(err, ErrFreeGiftRecipientUsed) {
		t.Fatalf("same phone, other email: err = %v", err)
	}
	st, err := f.gifts.Status(f.ctx, late.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Used || !st.Available {
		t.Fatalf("refused sender's status = %+v, want still available", st)
	}
	if held, _ := f.budget(o); held != freeGiftTestCost {
		t.Fatalf("budget held %d, want one gift", held)
	}
}

func (f *freeGiftFixture) race(o freeGiftOffer, n int, prefix string) (ok int32, errs []error) {
	f.t.Helper()
	senders := make([]uuid.UUID, n)
	recipients := make([]uuid.UUID, n)
	for i := range senders {
		senders[i] = f.sender()
		recipients[i] = f.newRecipient(senders[i])
	}
	var count atomic.Int32
	results := make([]error, n)
	var wg sync.WaitGroup
	for i := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.send(senders[i], recipients[i], o, fmt.Sprintf("%s-%02d", prefix, i)); err != nil {
				results[i] = err
				return
			}
			count.Add(1)
		}()
	}
	wg.Wait()
	for _, err := range results {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return count.Load(), errs
}

// Room in the budget for one more gift, ten customers at once.
func TestFreeGiftBudgetNeverPassesCap(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(freeGiftTestCost+freeGiftTestCost-1, 50)

	ok, errs := f.race(o, 10, "budget-race")
	if ok != 1 {
		t.Fatalf("%d orders, want 1", ok)
	}
	for _, err := range errs {
		if !errors.Is(err, ErrFreeGiftUnavailable) {
			t.Fatalf("loser: err = %v, want unavailable", err)
		}
	}
	if held, spent := f.budget(o); held+spent != freeGiftTestCost {
		t.Fatalf("held %d spent %d, want one gift", held, spent)
	}
	// The losers took nothing with them.
	if _, reserved := f.stock(o); reserved != 1 {
		t.Fatalf("reserved %d, want 1", reserved)
	}
	if n := f.int(`select gifts_count from promo.free_gift_products where product_id = $1`, o.product); n != 1 {
		t.Fatalf("gifts_count %d, want 1", n)
	}
	// And the product is no longer offered.
	offers, err := f.gifts.Available(f.ctx, FreeGiftFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, offer := range offers {
		if offer.ProductID == o.product {
			t.Fatal("product still listed with no budget left")
		}
	}
}

// The last unit of stock, ten customers at once.
func TestFreeGiftLastUnitOfStock(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(10000000, 1)

	ok, errs := f.race(o, 10, "stock-race")
	if ok != 1 {
		t.Fatalf("%d orders, want 1", ok)
	}
	for _, err := range errs {
		// The date check reads stock too, so a late loser is told there.
		if !errors.Is(err, ErrFreeGiftOutOfStock) && !errors.Is(err, ErrDeliveryDate) {
			t.Fatalf("loser: err = %v, want out of stock", err)
		}
	}
	if held, _ := f.budget(o); held != freeGiftTestCost {
		t.Fatalf("budget held %d, want one gift: a refused gift must hold nothing", held)
	}
	if available, reserved := f.stock(o); available != 1 || reserved != 1 {
		t.Fatalf("stock available %d reserved %d, want 1 and 1", available, reserved)
	}
}

func TestFreeGiftPerProductCap(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(10000000, 50)
	limit := 2
	if _, err := f.gifts.SetProduct(f.ctx, o.seller.String(), o.product, FreeGiftProductInput{GiftsCap: &limit}); err != nil {
		t.Fatal(err)
	}
	ok, _ := f.race(o, 6, "cap-race")
	if ok != 2 {
		t.Fatalf("%d orders, want the cap of 2", ok)
	}
	one := 1
	if _, err := f.gifts.SetProduct(f.ctx, o.seller.String(), o.product, FreeGiftProductInput{GiftsCap: &one}); !errors.Is(err, ErrFreeGiftCapBelowCount) {
		t.Fatalf("cap below gifts sent: err = %v", err)
	}
}

func TestFreeGiftCancelGivesEverythingBack(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(100000, 5)
	sender := f.sender()
	rec := f.newRecipient(sender)

	order, err := f.send(sender, rec, o, "cancel-key-1")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := f.orders.Cancel(f.ctx, sender.String(), order.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("status = %s", cancelled.Status)
	}
	if held, spent := f.budget(o); held != 0 || spent != 0 {
		t.Fatalf("budget held %d spent %d after cancel, want 0 and 0", held, spent)
	}
	if available, reserved := f.stock(o); available != 5 || reserved != 0 {
		t.Fatalf("stock available %d reserved %d after cancel, want 5 and 0", available, reserved)
	}
	if n := f.int(`select gifts_count from promo.free_gift_products where product_id = $1`, o.product); n != 0 {
		t.Fatalf("gifts_count %d after cancel, want 0", n)
	}
	if used, released := f.claims(order.ID, "send", "used")+f.claims(order.ID, "receive", "used"),
		f.claims(order.ID, "send", "released")+f.claims(order.ID, "receive", "released"); used != 0 || released != 5 {
		t.Fatalf("claims: %d used, %d released; want 0 and 5", used, released)
	}

	// Cancelling again changes nothing.
	if _, err := f.orders.Cancel(f.ctx, sender.String(), order.ID.String()); !errors.Is(err, ErrOrderNotCancellable) {
		t.Fatalf("second cancel: err = %v", err)
	}
	if held, _ := f.budget(o); held != 0 {
		t.Fatalf("budget held %d after second cancel", held)
	}

	// The free gift is back, for the sender and for the recipient.
	st, err := f.gifts.Status(f.ctx, sender.String(), rec.String())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Available || st.Recipient == nil || !st.Recipient.CanReceive {
		t.Fatalf("status after cancel = %+v / %+v", st, st.Recipient)
	}
	if _, err := f.send(sender, rec, o, "cancel-key-2"); err != nil {
		t.Fatalf("send after cancel: %v", err)
	}
}

func TestFreeGiftDeliveredSpendsBudget(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(100000, 5)
	sender := f.sender()

	order, err := f.send(sender, f.newRecipient(sender), o, "deliver-key-1")
	if err != nil {
		t.Fatal(err)
	}
	item := order.Items[0].ID
	f.acceptAll(o.seller, []uuid.UUID{item})
	if _, err := f.shipping.StartLocalDelivery(f.ctx, o.seller.String(), item.String(), LocalDeliveryInput{}); err != nil {
		t.Fatal(err)
	}
	// Once the seller has dispatched it, it can no longer be cancelled.
	if _, err := f.orders.Cancel(f.ctx, sender.String(), order.ID.String()); !errors.Is(err, ErrOrderNotCancellable) {
		t.Fatalf("cancel after dispatch: err = %v", err)
	}
	if held, spent := f.budget(o); held != freeGiftTestCost || spent != 0 {
		t.Fatalf("in transit: held %d spent %d", held, spent)
	}

	if _, err := f.shipping.CompleteLocalDelivery(f.ctx, o.seller.String(), item.String()); err != nil {
		t.Fatal(err)
	}
	if held, spent := f.budget(o); held != 0 || spent != freeGiftTestCost {
		t.Fatalf("delivered: held %d spent %d, want 0 and %d", held, spent, freeGiftTestCost)
	}
	if available, reserved := f.stock(o); available != 4 || reserved != 0 {
		t.Fatalf("delivered: stock available %d reserved %d, want 4 and 0", available, reserved)
	}
	if used := f.claims(order.ID, "send", "used") + f.claims(order.ID, "receive", "used"); used != 5 {
		t.Fatalf("delivered: %d rights still used, want all 5 kept", used)
	}
	var status string
	f.scan(&status, `select status from marketplace.orders where id = $1`, order.ID)
	if status != "delivered" {
		t.Fatalf("order status = %s", status)
	}
}

func TestFreeGiftPauseStopAndBudgetChange(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(100000, 5)
	first := f.sender()
	confirmed, err := f.send(first, f.newRecipient(first), o, "pause-key-1")
	if err != nil {
		t.Fatal(err)
	}

	// Lowering the budget under what is held is refused with a clear error.
	if _, err := f.gifts.AdminSetProgram(f.ctx, o.seller, AdminProgramInput{
		BudgetCap: freeGiftTestCost - 1, Currency: "USD", Status: "active",
	}); !errors.Is(err, ErrFreeGiftBudgetBelowCommitted) {
		t.Fatalf("budget below held: err = %v", err)
	}
	if _, err := f.gifts.AdminSetProgram(f.ctx, o.seller, AdminProgramInput{
		BudgetCap: 100000, Currency: "AUD", Status: "active",
	}); !errors.Is(err, ErrFreeGiftCurrencyLocked) {
		t.Fatalf("currency change with gifts: err = %v", err)
	}

	// Paused by the seller: new gifts are refused.
	if _, err := f.gifts.SetProgram(f.ctx, o.seller.String(), FreeGiftProgramInput{Status: "paused"}); err != nil {
		t.Fatal(err)
	}
	second := f.sender()
	rec := f.newRecipient(second)
	if _, err := f.send(second, rec, o, "pause-key-2"); !errors.Is(err, ErrFreeGiftUnavailable) {
		t.Fatalf("send while paused: err = %v", err)
	}

	// Stopped by an admin: the seller cannot turn it back on.
	if _, err := f.gifts.AdminSetStopped(f.ctx, o.seller, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.gifts.SetProgram(f.ctx, o.seller.String(), FreeGiftProgramInput{Status: "active"}); !errors.Is(err, ErrFreeGiftProgramStopped) {
		t.Fatalf("seller re-activating a stopped program: err = %v", err)
	}
	if _, err := f.send(second, rec, o, "pause-key-3"); !errors.Is(err, ErrFreeGiftUnavailable) {
		t.Fatalf("send while stopped: err = %v", err)
	}

	// The gift confirmed before all this is still fulfilled.
	item := confirmed.Items[0].ID
	f.acceptAll(o.seller, []uuid.UUID{item})
	if _, err := f.shipping.StartLocalDelivery(f.ctx, o.seller.String(), item.String(), LocalDeliveryInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.shipping.CompleteLocalDelivery(f.ctx, o.seller.String(), item.String()); err != nil {
		t.Fatal(err)
	}
	if held, spent := f.budget(o); held != 0 || spent != freeGiftTestCost {
		t.Fatalf("fulfilled while stopped: held %d spent %d", held, spent)
	}

	// Re-allowed leaves it paused; the seller turns it on, and gifts flow.
	p, err := f.gifts.AdminSetStopped(f.ctx, o.seller, false)
	if err != nil || p.Status != models.FreeGiftProgramPaused {
		t.Fatalf("re-allow = %+v, %v", p, err)
	}
	if _, err := f.gifts.SetProgram(f.ctx, o.seller.String(), FreeGiftProgramInput{Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.send(second, rec, o, "pause-key-4"); err != nil {
		t.Fatalf("send after re-allow: %v", err)
	}
}

func TestFreeGiftSellerSetupRules(t *testing.T) {
	f := newFreeGiftFixture(t)
	shop := f.shop(f.seller(), freeGiftTestPrice)
	seller := shop.seller.String()

	if p, err := f.gifts.Program(f.ctx, seller); err != nil || p.Status != models.FreeGiftProgramNotSet {
		t.Fatalf("program before setup = %+v, %v", p, err)
	}
	// A product cannot be chosen before the budget is set.
	if _, err := f.gifts.SetProduct(f.ctx, seller, shop.product, FreeGiftProductInput{}); !errors.Is(err, ErrFreeGiftProgramNotFound) {
		t.Fatalf("product before program: err = %v", err)
	}
	sellerID := shop.seller
	// The seller cannot turn a program on before agreeing to the marketing
	// budget, and an admin cannot set a budget for a seller who has not.
	if _, err := f.gifts.SetProgram(f.ctx, seller, FreeGiftProgramInput{Status: "active"}); !errors.Is(err, ErrFreeGiftTermsRequired) {
		t.Fatalf("seller activating without terms: err = %v", err)
	}
	budget := AdminProgramInput{BudgetCap: 50000, Currency: "usd", Status: "active"}
	if _, err := f.gifts.AdminSetProgram(f.ctx, sellerID, budget); !errors.Is(err, ErrFreeGiftSellerTermsMissing) {
		t.Fatalf("admin budget without seller terms: err = %v", err)
	}
	// Agreed, but the admin has not set a budget yet: nothing to switch on.
	if _, err := f.gifts.SetProgram(f.ctx, seller, FreeGiftProgramInput{Status: "active", AcceptTerms: true}); !errors.Is(err, ErrFreeGiftProgramNotFound) {
		t.Fatalf("seller activating before a budget: err = %v", err)
	}
	p, err := f.gifts.AdminSetProgram(f.ctx, sellerID, budget)
	if err != nil {
		t.Fatal(err)
	}
	if p.Currency != "USD" || p.BudgetRemaining != 50000 || p.AcceptedAt == nil || p.TermsVersion == nil {
		t.Fatalf("program = %+v", p)
	}

	// Someone else's product, and one in another currency, cannot join.
	other := f.shop(f.seller(), 500)
	if _, err := f.gifts.SetProduct(f.ctx, seller, other.product, FreeGiftProductInput{}); !errors.Is(err, ErrFreeGiftProductNotFound) {
		t.Fatalf("another seller's product: err = %v", err)
	}
	f.exec(`update seller.products set currency = 'AUD' where id = $1`, shop.product)
	if _, err := f.gifts.SetProduct(f.ctx, seller, shop.product, FreeGiftProductInput{}); !errors.Is(err, ErrFreeGiftProductNotEligible) {
		t.Fatalf("product in another currency: err = %v", err)
	}
	f.exec(`update seller.products set currency = 'USD' where id = $1`, shop.product)

	if _, err := f.gifts.SetProduct(f.ctx, seller, shop.product, FreeGiftProductInput{}); err != nil {
		t.Fatal(err)
	}
	list, err := f.gifts.Products(f.ctx, seller)
	if err != nil || len(list) != 1 || list[0].ProductID != shop.product || list[0].StockLeft != nil {
		t.Fatalf("products = %+v, %v", list, err)
	}

	// With no stock set it is in the program but not offered, and cannot be sent.
	sender := f.sender()
	if _, err := f.send(sender, f.newRecipient(sender), freeGiftOffer{shop}, "setup-no-stock"); !errors.Is(err, ErrFreeGiftOutOfStock) {
		t.Fatalf("send with no stock row: err = %v", err)
	}
	if held, _ := f.budget(freeGiftOffer{shop}); held != 0 {
		t.Fatalf("budget held %d after an out-of-stock refusal", held)
	}

	if err := f.gifts.RemoveProduct(f.ctx, seller, shop.product); err != nil {
		t.Fatal(err)
	}
	if err := f.gifts.RemoveProduct(f.ctx, seller, shop.product); !errors.Is(err, ErrFreeGiftProductNotFound) {
		t.Fatalf("removing twice: err = %v", err)
	}
}

// A closed account does not hand its free gift to the next one on the same
// phone and email.
func TestFreeGiftNotResetByNewAccount(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(1000000, 5)
	first := f.sender()
	if _, err := f.send(first, f.newRecipient(first), o, "reset-key-1"); err != nil {
		t.Fatal(err)
	}
	var email, phone string
	if err := f.pool.QueryRow(f.ctx, `select email::text, phone_e164 from customer.customers where id = $1`, first).
		Scan(&email, &phone); err != nil {
		t.Fatal(err)
	}
	f.exec(`update customer.customers
		set deleted_at = now(), status = 'deleted', email = $2, phone_verified_at = null where id = $1`,
		first, "closed-"+email)

	// Same phone, new email.
	var samePhone uuid.UUID
	f.scan(&samePhone, `insert into customer.customers
		(country_id, email, password_hash, phone, phone_e164, phone_verified_at, email_verified_at)
		values ($1, $2, 'x', $3, $3, now(), now()) returning id`, f.country, uuid.NewString()+"@example.test", phone)
	f.customers = append(f.customers, samePhone)
	if _, err := f.send(samePhone, f.newRecipient(samePhone), o, "reset-key-2"); !errors.Is(err, ErrFreeGiftAlreadyUsed) {
		t.Fatalf("new account on the same phone: err = %v", err)
	}

	// Same email, new phone.
	var sameEmail uuid.UUID
	f.scan(&sameEmail, `insert into customer.customers
		(country_id, email, password_hash, phone, phone_e164, phone_verified_at, email_verified_at)
		values ($1, $2, 'x', $3, $3, now(), now()) returning id`, f.country, email, f.phone())
	f.customers = append(f.customers, sameEmail)
	if _, err := f.send(sameEmail, f.newRecipient(sameEmail), o, "reset-key-3"); !errors.Is(err, ErrFreeGiftAlreadyUsed) {
		t.Fatalf("new account on the same email: err = %v", err)
	}
	st, err := f.gifts.Status(f.ctx, sameEmail.String(), "")
	if err != nil || !st.Used {
		t.Fatalf("status = %+v, %v; want used", st, err)
	}
}

func TestFreeGiftDisabledAndBadKey(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(100000, 5)
	sender := f.sender()
	rec := f.newRecipient(sender)

	if _, err := f.send(sender, rec, o, ""); !errors.Is(err, ErrIdempotencyKeyRequired) {
		t.Fatalf("no key: err = %v", err)
	}
	off := *f.gifts
	off.enabled = false
	if _, err := off.Send(f.ctx, sender.String(), "disabled-key", FreeGiftSendInput{}); !errors.Is(err, ErrFreeGiftDisabled) {
		t.Fatalf("flag off: err = %v", err)
	}
}

// Paid orders are written exactly as before.
func TestFreeGiftLeavesPaidOrdersAlone(t *testing.T) {
	f := newFreeGiftFixture(t)
	o := f.offer(100000, 5)

	res, err := f.placeOrder(nil, nil, o.testShop)
	if err != nil {
		t.Fatal(err)
	}
	if res.total != freeGiftTestPrice+freeGiftTestDelivery || res.total != res.subtotal+res.delivery {
		t.Fatalf("paid total %d subtotal %d delivery %d", res.total, res.subtotal, res.delivery)
	}
	var status, kind, budget string
	var cost int
	var key *string
	if err := f.pool.QueryRow(f.ctx, `
		select status, order_kind, free_gift_budget_status, free_gift_cost, idempotency_key
		from marketplace.orders where id = $1`, res.id).Scan(&status, &kind, &budget, &cost, &key); err != nil {
		t.Fatal(err)
	}
	if status != "pending_payment" || kind != "paid" || budget != "none" || cost != 0 || key != nil {
		t.Fatalf("paid order = %s / %s / %s / %d / %v", status, kind, budget, cost, key)
	}
	// It touched no budget and reserved no stock, as before.
	if held, spent := f.budget(o); held != 0 || spent != 0 {
		t.Fatalf("paid order moved the budget: held %d spent %d", held, spent)
	}
	if _, reserved := f.stock(o); reserved != 0 {
		t.Fatalf("paid order reserved stock: %d", reserved)
	}
	if n := f.int(`select count(*) from promo.free_gift_claims where order_id = $1`, res.id); n != 0 {
		t.Fatalf("paid order wrote %d claims", n)
	}
	// Cancelling it gives nothing back to a budget it never used.
	if err := repository.NewOrderRepository(f.pool).CancelForCustomer(f.ctx, f.customer.String(), res.id.String()); err != nil {
		t.Fatal(err)
	}
	if held, spent := f.budget(o); held != 0 || spent != 0 {
		t.Fatalf("cancelling a paid order moved the budget: held %d spent %d", held, spent)
	}
}
