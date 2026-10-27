package services

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// Member product discounts: the member pays less, the seller's list price and
// the base their payout is worked out on do not move, and the discount can
// never be more than the platform commission that funds it.

func (f *pointsFixture) memberPeriod(customer uuid.UUID, discountBPS int, start, end time.Time) {
	f.t.Helper()
	var offer, sub uuid.UUID
	f.scan(&offer, `insert into membership.plan_offers
		(tier_code, version, country_id, name, currency, price_minor, interval_unit, interval_count,
		 member_bonus_points, member_discount_bps, terms_version, status, effective_from, published_at,
		 submitted_by_admin_id, approved_by_admin_id)
		values ($1, 1, $2, 'Plus', 'USD', 999, 'month', 1, 0, $3, 'v1', 'published', now(), now(),
		        gen_random_uuid(), gen_random_uuid()) returning id`,
		"md-"+uuid.NewString()[:8], f.country, discountBPS)
	f.scan(&sub, `insert into membership.subscriptions
		(customer_id, offer_id, provider, status, renewal_consent_at, idempotency_key)
		values ($1, $2, 'test', 'active', now(), $3) returning id`, customer, offer, uuid.NewString())
	f.exec(`insert into membership.paid_periods
		(customer_id, subscription_id, offer_id, period_start, period_end, provider_invoice_id, amount_minor, currency, paid_at)
		values ($1, $2, $3, $4, $5, $6, 999, 'USD', $4)`, customer, sub, offer, start, end, "inv-"+uuid.NewString())
}

func TestMemberDiscountPricing(t *testing.T) {
	f := newPointsFixture(t, "manual")
	cp := repository.NewCompetitionPointsRepository(f.pool)
	// SendAGift's commission is 7.9%.
	f.orders.UseMemberDiscounts(cp.MemberDiscount, 790)
	seller := f.seller()
	product := f.product(seller, 0)
	ctx := context.Background()
	now := time.Now()

	type totals struct{ subtotal, discount, total, line int }
	place := func(customer uuid.UUID) totals {
		t.Helper()
		o, err := f.order(customer, product, 1, nil, 0)
		if err != nil {
			t.Fatalf("order: %v", err)
		}
		var disc int
		f.scan(&disc, `select member_discount_amount from marketplace.orders where id = $1`, o.Order.ID)
		return totals{o.Order.SubtotalAmount, o.Order.MemberDiscountAmount, o.Order.TotalAmount, o.Items[0].TotalAmount}
	}

	// Not a member: nothing changes.
	plain := place(f.customer(""))
	if plain.discount != 0 || plain.total != plain.subtotal {
		t.Fatalf("non-member: %+v", plain)
	}

	// A member at 5%: pays 5% less on the items; the line, which the seller
	// is paid on, stays at the list price.
	member := f.customer("")
	f.memberPeriod(member, 500, now.Add(-time.Hour), now.Add(24*time.Hour))
	got := place(member)
	want := plain.subtotal * 500 / 10000
	if got.discount != want || got.total != plain.total-want {
		t.Fatalf("member 5%%: %+v, want discount %d", got, want)
	}
	if got.line != plain.line || got.subtotal != plain.subtotal {
		t.Fatalf("the seller's list price moved: %+v vs %+v", got, plain)
	}
	// The payout base is the line total, which has not changed.
	var base, stored int
	f.scan(&base, `select coalesce(sum(oi.total_amount), 0) from marketplace.order_items oi
		join marketplace.orders o on o.id = oi.order_id where o.customer_id = $1`, member)
	f.scan(&stored, `select coalesce(sum(oi.member_discount_amount), 0) from marketplace.order_items oi
		join marketplace.orders o on o.id = oi.order_id where o.customer_id = $1`, member)
	if base != plain.line || stored != want {
		t.Fatalf("payout base %d (want %d), line discounts %d (want %d)", base, plain.line, stored, want)
	}
	if bps, err := f.orders.MemberDiscountFor(ctx, member); err != nil || bps != 500 {
		t.Fatalf("preview: %d %v", bps, err)
	}

	// An offer set above the commission (say the commission was lowered
	// later) is capped at it when the order is priced.
	capped := f.customer("")
	f.memberPeriod(capped, 1500, now.Add(-time.Hour), now.Add(24*time.Hour))
	c := place(capped)
	if want := plain.subtotal * 790 / 10000; c.discount != want {
		t.Fatalf("capped: discount %d, want %d", c.discount, want)
	}

	// A period that has ended gives nothing.
	lapsed := f.customer("")
	f.memberPeriod(lapsed, 500, now.Add(-48*time.Hour), now.Add(-time.Hour))
	if l := place(lapsed); l.discount != 0 {
		t.Fatalf("lapsed period still discounted: %+v", l)
	}

	// With no commission set there is nothing to fund a discount from.
	f.orders.UseMemberDiscounts(cp.MemberDiscount, 0)
	if z := place(member); z.discount != 0 {
		t.Fatalf("no commission: %+v", z)
	}
}

// The discount is capped by the platform commission when an offer is saved.
func TestMemberDiscountOfferCap(t *testing.T) {
	f := newCPFixture(t, "IN")
	f.cp.platformFeeBPS = 790
	offer := func(bps int) (*OfferInput, error) {
		in := OfferInput{
			TierCode: "cap-" + uuid.NewString()[:6], CountryID: f.country.String(), Name: "Capped",
			Currency: f.currency, PriceMinor: 999, IntervalUnit: "month", IntervalCount: 1,
			MemberBonusPoints: 0, MemberDiscountBPS: bps, TermsVersion: "v1",
		}
		_, err := f.cp.CreateOffer(f.ctx, f.adminA, in)
		return &in, err
	}
	if _, err := offer(790); err != nil {
		t.Fatalf("a discount equal to the commission must be allowed: %v", err)
	}
	if _, err := offer(791); err == nil {
		t.Fatalf("a discount above the 7.9%% commission must be refused")
	}
	if _, err := offer(-1); err == nil {
		t.Fatalf("a negative discount must be refused")
	}
	// No commission set: nothing can fund a discount.
	f.cp.platformFeeBPS = 0
	if _, err := offer(100); err == nil {
		t.Fatalf("a discount with no commission must be refused")
	}
	if _, err := offer(0); err != nil {
		t.Fatalf("no discount is always fine: %v", err)
	}
	// The saved offer keeps its discount and cannot be edited once submitted.
	f.cp.platformFeeBPS = 500
	o, err := f.cp.CreateOffer(f.ctx, f.adminA, OfferInput{
		TierCode: "keep-" + uuid.NewString()[:6], CountryID: f.country.String(), Name: "Keep", Currency: f.currency,
		PriceMinor: 999, IntervalUnit: "month", IntervalCount: 1, MemberDiscountBPS: 250, TermsVersion: "v1"})
	if err != nil || o.MemberDiscountBPS != 250 {
		t.Fatalf("saved offer: %+v %v", o, err)
	}
	if _, err := f.cp.OfferTransition(f.ctx, f.adminA, o.ID, "submit"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `update membership.plan_offers set member_discount_bps = 500 where id = $1`, o.ID); err == nil {
		t.Fatalf("the database must refuse changing a submitted offer's discount")
	}
}

// A Super Admin can publish a membership package alone: no second approver.
func TestMembershipPublishDirectly(t *testing.T) {
	f := newCPFixture(t, "PH")
	mk := func(name string) *OfferInput {
		return &OfferInput{
			TierCode: "direct-" + uuid.NewString()[:6], CountryID: f.country.String(), Name: name, Currency: f.currency,
			PriceMinor: 999, IntervalUnit: "month", IntervalCount: 1, TermsVersion: "v1",
		}
	}
	in := mk("Direct")
	o, err := f.cp.CreateOffer(f.ctx, f.adminA, *in)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := f.cp.OfferTransition(f.ctx, f.adminA, o.ID, "publish")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Status != "published" || pub.PublishedAt == nil || pub.ApprovedByAdminID == nil {
		t.Fatalf("published offer: %+v", pub)
	}
	// It is on sale right away, and from then on it cannot be edited.
	list, err := f.repo.PublishedOffers(f.ctx, f.country)
	if err != nil || len(list) == 0 {
		t.Fatalf("on sale: %v %v", list, err)
	}
	if _, err := f.cp.UpdateOffer(f.ctx, f.adminA, o.ID, *in); err == nil {
		t.Fatalf("a published offer must be locked")
	}
	// Publishing opens the country: sales, renewals and gift earning are
	// approved, so customers there are offered it.
	var opened int
	f.scan(&opened, `select count(*) from core.capability_approvals where country_id = $1 and status = 'active'
		and self_approved and operation in ('membership_sale', 'membership_renewal', 'gift_points_earning')`, f.country)
	if opened != 3 {
		t.Fatalf("publishing should open 3 operations, opened %d", opened)
	}
	cust := f.customer()
	offers, err := f.cp.Offers(f.ctx, cust, "web")
	if err != nil || !offers.Decision.Allowed || len(offers.Offers) == 0 {
		t.Fatalf("offered after publish: %+v %v", offers, err)
	}
	// Only a draft can be published this way.
	if _, err := f.cp.OfferTransition(f.ctx, f.adminA, o.ID, "publish"); err == nil {
		t.Fatalf("publishing twice must be refused")
	}
	// A new version of the same tier replaces it.
	next, err := f.cp.CreateOffer(f.ctx, f.adminA, OfferInput{
		TierCode: o.TierCode, CountryID: f.country.String(), Name: "Direct v2", Currency: f.currency,
		PriceMinor: 1299, IntervalUnit: "month", IntervalCount: 1, TermsVersion: "v1"})
	if err != nil || next.Version != 2 {
		t.Fatalf("v2: %+v %v", next, err)
	}
	if _, err := f.cp.OfferTransition(f.ctx, f.adminA, next.ID, "publish"); err != nil {
		t.Fatal(err)
	}
	var status string
	f.scan(&status, `select status from membership.plan_offers where id = $1`, o.ID)
	if status != "retired" {
		t.Fatalf("the replaced version should be retired, is %s", status)
	}
}

// A paid Stripe membership is confirmed by asking Stripe, even when no
// webhook arrives: the page Stripe returns to syncs it, and the bonus of a
// package credited on payment lands once, however often it is synced.
func TestMembershipSyncFromStripe(t *testing.T) {
	f := newCPFixture(t, "PH")
	var sessionID, localID string
	// Stripe ids are unique; a fresh one per run keeps earlier runs' rows apart.
	subID := "sub_" + uuid.NewString()[:8]
	now := time.Now().Unix()
	stripe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/checkout/sessions":
			_ = r.ParseForm()
			sessionID = "cs_test_" + uuid.NewString()[:8]
			localID = r.PostForm.Get("subscription_data[metadata][membership_subscription_id]")
			_, _ = fmt.Fprintf(w, `{"id":%q,"url":"https://checkout.stripe.test/%s"}`, sessionID, sessionID)
		case r.Method == http.MethodGet && r.URL.Path == "/checkout/sessions/"+sessionID:
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":"complete","payment_status":"paid","subscription":%q,"customer":"cus_1"}`, sessionID, subID)
		case r.Method == http.MethodGet && r.URL.Path == "/subscriptions/"+subID:
			_, _ = fmt.Fprintf(w, `{"id":%q,"customer":"cus_1","status":"active",
				"metadata":{"kind":"membership","membership_subscription_id":%q},
				"items":{"data":[{"current_period_end":%d}]}}`, subID, localID, now+30*86400)
		case r.Method == http.MethodGet && r.URL.Path == "/invoices":
			if r.URL.Query().Get("subscription") != subID {
				http.Error(w, "bad subscription", 400)
				return
			}
			_, _ = fmt.Fprintf(w, `{"data":[{"id":"in_%s","status":"paid","amount_paid":999,"currency":"php",
				"payment_intent":"pi_%s","status_transitions":{"paid_at":%d},
				"parent":{"subscription_details":{"subscription":%q,
				  "metadata":{"kind":"membership","membership_subscription_id":%q}}},
				"lines":{"data":[{"period":{"start":%d,"end":%d}}]}}]}`,
				subID, subID, now, subID, localID, now-60, now+30*86400)
		default:
			http.NotFound(w, r)
		}
	}))
	defer stripe.Close()
	client := NewStripeClient("sk_test_x")
	client.base = stripe.URL
	f.cp = NewCompetitionPointsService(f.repo, f.comps, client, CompetitionPointsConfig{
		WebURL: "http://localhost:5173", QuoteSecret: "test-quote-secret", HoldTTL: 10 * time.Minute,
		RestoreWindow: time.Hour,
	})

	o, err := f.cp.CreateOffer(f.ctx, f.adminA, OfferInput{
		TierCode: "sync-" + uuid.NewString()[:6], CountryID: f.country.String(), Name: "Plus", Currency: f.currency,
		PriceMinor: 999, IntervalUnit: "month", IntervalCount: 1, MemberBonusPoints: 1000, TermsVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.cp.OfferTransition(f.ctx, f.adminA, o.ID, "publish"); err != nil {
		t.Fatal(err)
	}
	member := f.customer()
	res, err := f.cp.Join(f.ctx, member, "web", JoinInput{OfferID: o.ID, RenewalConsent: true, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if !strings.Contains(res.CheckoutURL, sessionID) {
		t.Fatalf("checkout url %s", res.CheckoutURL)
	}
	for i := 0; i < 2; i++ {
		st, err := f.cp.SyncMembership(f.ctx, member)
		if err != nil {
			t.Fatalf("sync: %v", err)
		}
		if st.CurrentPeriod == nil || st.State != models.MembershipBonusUnlocked {
			t.Fatalf("after sync %d: %s %+v", i, st.State, st.CurrentPeriod)
		}
		if got := f.available(member); got != 1000 {
			t.Fatalf("after sync %d: %d points, want 1000", i, got)
		}
	}
	f.reconciled()
}
