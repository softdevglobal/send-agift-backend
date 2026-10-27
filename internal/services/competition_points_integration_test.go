package services

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/database"
	"myapp/internal/models"
	"myapp/internal/repository"
)

// Acceptance scenarios for competition points (handover v4, Appendix A),
// against a real Postgres because what is under test (row locks, unique
// keys, deferred balance checks) lives in the database.
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
//	    go test ./internal/services -run CompetitionPoints -v
//
// Each test uses a real country code, so the residence rule (a verified
// phone number from the account's country) runs as it does in production.

type cpFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	repo     *repository.CompetitionPointsRepository
	cp       *CompetitionPointsService
	comps    *CompetitionService
	adminA   AdminActor
	adminB   AdminActor
	country  uuid.UUID
	iso      string
	currency string
	prefix   string // mobile number prefix for the country
	digits   int
	saleOK   bool
}

// Each country with a mobile prefix and how many digits follow it.
var cpCountries = map[string]struct {
	currency, prefix string
	digits           int
}{
	"NZ": {"NZD", "+6421", 7}, "IE": {"EUR", "+35385", 7}, "GB": {"GBP", "+447400", 6},
	"NL": {"EUR", "+316", 8}, "SE": {"SEK", "+4670", 7}, "ZA": {"ZAR", "+2782", 7},
	"SG": {"SGD", "+658", 7}, "MY": {"MYR", "+6012", 7}, "IN": {"INR", "+9198", 8},
	"PH": {"PHP", "+63917", 7}, "DE": {"EUR", "+49151", 8}, "FR": {"EUR", "+336", 8},
}

func newCPFixture(t *testing.T, iso string) *cpFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping competition points tests")
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
	c := cpCountries[iso]
	f := &cpFixture{t: t, ctx: ctx, pool: pool, iso: iso, currency: c.currency, prefix: c.prefix, digits: c.digits,
		adminA: AdminActor{ID: uuid.New(), SuperAdmin: true}, adminB: AdminActor{ID: uuid.New(), SuperAdmin: true}}
	f.scan(&f.country, `
		insert into core.countries (iso_code, name, default_currency, default_timezone)
		values ($1, $2, $3, 'UTC')
		on conflict (iso_code) do update set name = excluded.name returning id`, iso, "CP "+iso, c.currency)
	f.exec(`
		insert into core.country_capabilities
			(country_id, skill_competitions_enabled, progressive_prizes_enabled, points_usage_enabled,
			 points_earning_enabled, chance_games_enabled)
		values ($1, true, true, true, true, false)
		on conflict (country_id) do update set skill_competitions_enabled = true,
			progressive_prizes_enabled = true, points_usage_enabled = true`, f.country)
	// Approvals and stops from an earlier run of a test would leak into
	// this one.
	f.exec(`update core.capability_approvals set status = 'revoked', revoked_at = now(),
		revoke_reason = 'test reset' where country_id = $1 and status in ('pending', 'active')`, f.country)
	f.exec(`update core.emergency_stops set lifted_at = now(), lifted_by_admin_id = $2, lift_reason = 'test reset'
		where lifted_at is null and (country_id = $1 or country_id is null)`, f.country, f.adminA.ID)

	f.repo = repository.NewCompetitionPointsRepository(pool)
	pointsRepo := repository.NewPointsRepository(pool)
	f.comps = NewCompetitionService(repository.NewCompetitionRepository(pool), repository.NewGameRepository(pool),
		repository.NewCustomerRepository(pool), repository.NewCountryCapabilityRepository(pool),
		repository.NewCountryRepository(pool), pointsRepo)
	f.cp = NewCompetitionPointsService(f.repo, f.comps, nil, CompetitionPointsConfig{
		WebURL: "http://localhost:5173", QuoteSecret: "test-quote-secret", HoldTTL: 10 * time.Minute,
		RestoreWindow: time.Hour, TestPayments: true,
	})
	return f
}

func (f *cpFixture) scan(dest any, sql string, args ...any) {
	f.t.Helper()
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(dest); err != nil {
		f.t.Fatalf("scan: %v\n%s", err, sql)
	}
}

func (f *cpFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("exec: %v\n%s", err, sql)
	}
}

func (f *cpFixture) count(sql string, args ...any) int64 {
	f.t.Helper()
	var n int64
	f.scan(&n, sql, args...)
	return n
}

func randDigits(n int) string {
	out := make([]byte, n)
	for i := range out {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		out[i] = byte('0' + d.Int64())
	}
	return string(out)
}

// customer is an active account resident in the fixture's country: a
// verified mobile number from it and an adult date of birth.
func (f *cpFixture) customer() uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	f.scan(&id, `insert into customer.customers
		(country_id, email, password_hash, display_name, date_of_birth, phone_e164, phone_verified_at,
		 age_verified_at, identity_verified_at)
		values ($1, $2, 'x', 'CP Customer', '1990-01-01', $3, now(), now(), now()) returning id`,
		f.country, uuid.NewString()+"@cp.test", f.prefix+randDigits(f.digits))
	return id
}

// approve records and approves an operation, by two different admins.
func (f *cpFixture) approve(op, channel string, provider *string) {
	f.t.Helper()
	expires := time.Now().Add(365 * 24 * time.Hour)
	a, err := f.cp.RequestApproval(f.ctx, f.adminA, ApprovalInput{
		Operation: op, CountryID: f.country.String(), Channel: channel, Provider: provider,
		EvidenceRef: "TEST-EVIDENCE-1", ExpiresAt: &expires,
	})
	if err != nil {
		f.t.Fatalf("request approval %s: %v", op, err)
	}
	if _, err := f.cp.DecideApproval(f.ctx, f.adminB, a.ID, true); err != nil {
		f.t.Fatalf("approve %s: %v", op, err)
	}
}

// policy publishes a fixed-award reward policy for the country.
func (f *cpFixture) policy(base int64) *models.RewardPolicy {
	f.t.Helper()
	p, err := f.cp.CreatePolicy(f.ctx, f.adminA, PolicyInput{
		CountryID: f.country.String(), BaseMode: "fixed", BaseFixedPoints: base,
	})
	if err != nil {
		f.t.Fatalf("policy: %v", err)
	}
	if _, err := f.cp.PolicyTransition(f.ctx, f.adminA, p.ID, "submit"); err != nil {
		f.t.Fatalf("submit policy: %v", err)
	}
	p, err = f.cp.PolicyTransition(f.ctx, f.adminB, p.ID, "approve")
	if err != nil {
		f.t.Fatalf("approve policy: %v", err)
	}
	return p
}

// offer publishes a monthly membership with the given extra bonus.
func (f *cpFixture) offer(bonus int64) *models.PlanOffer {
	f.t.Helper()
	o, err := f.cp.CreateOffer(f.ctx, f.adminA, OfferInput{
		TierCode: "plus-" + uuid.NewString()[:6], CountryID: f.country.String(), Name: "Plus",
		Currency: f.currency, PriceMinor: 999, IntervalUnit: "month", IntervalCount: 1,
		MemberBonusPoints: bonus, BonusTrigger: models.BonusTriggerFirstGift, TermsVersion: "v1",
		GiftBenefits: []string{"Free delivery"},
	})
	if err != nil {
		f.t.Fatalf("offer: %v", err)
	}
	if _, err := f.cp.OfferTransition(f.ctx, f.adminA, o.ID, "submit"); err != nil {
		f.t.Fatalf("submit offer: %v", err)
	}
	o, err = f.cp.OfferTransition(f.ctx, f.adminB, o.ID, "approve")
	if err != nil {
		f.t.Fatalf("approve offer: %v", err)
	}
	return o
}

// earning approves gift earning and publishes a policy.
func (f *cpFixture) earning(base int64) {
	f.approve(models.OpGiftPointsEarning, "any", nil)
	f.policy(base)
}

// join makes the customer a member through the test provider and pays the
// first period.
func (f *cpFixture) join(customer uuid.UUID, o *models.PlanOffer) {
	f.t.Helper()
	if !f.saleOK {
		test := "test"
		f.approve(models.OpMembershipSale, "web", &test)
		f.approve(models.OpMembershipRenewal, "any", &test)
		f.saleOK = true
	}
	if _, err := f.cp.Join(f.ctx, customer, "web", JoinInput{
		OfferID: o.ID, RenewalConsent: true, IdempotencyKey: uuid.NewString()}); err != nil {
		f.t.Fatalf("join: %v", err)
	}
	if _, err := f.cp.TestPay(f.ctx, customer); err != nil {
		f.t.Fatalf("test pay: %v", err)
	}
}

// gift is a paid gift order, paid now, not yet delivered.
func (f *cpFixture) gift(customer uuid.UUID, subtotal int64) uuid.UUID {
	f.t.Helper()
	return f.giftPaidAt(customer, subtotal, time.Now())
}

func (f *cpFixture) giftPaidAt(customer uuid.UUID, subtotal int64, paidAt time.Time) uuid.UUID {
	f.t.Helper()
	var order uuid.UUID
	f.scan(&order, `insert into marketplace.orders
		(order_number, customer_id, country_id, delivery_date, status, subtotal_amount, total_amount, currency)
		values ($1, $2, $3, current_date + 2, 'paid', $4, $4, $5) returning id`,
		"CP-"+uuid.NewString()[:12], customer, f.country, subtotal, f.currency)
	f.exec(`insert into finance.payments
		(order_id, customer_id, provider, provider_payment_id, amount, currency, status, idempotency_key, paid_at)
		values ($1, $2, 'stripe', $3, $4, $5, 'succeeded', $6, $7)`,
		order, customer, "pi_"+uuid.NewString(), subtotal, f.currency, uuid.NewString(), paidAt)
	return order
}

func (f *cpFixture) deliver(order uuid.UUID) {
	f.exec(`update marketplace.orders set status = 'delivered', updated_at = now() where id = $1`, order)
}

func (f *cpFixture) refund(order uuid.UUID) {
	f.exec(`update finance.payments set status = 'refunded' where order_id = $1`, order)
	f.exec(`update marketplace.orders set status = 'refunded', updated_at = now() where id = $1`, order)
}

func (f *cpFixture) run() *models.CPRunResult {
	f.t.Helper()
	res, err := f.cp.RunOnce(f.ctx)
	if err != nil {
		f.t.Fatalf("run: %v", err)
	}
	return res
}

func (f *cpFixture) lots(customer uuid.UUID, source string) (n, total int64) {
	f.t.Helper()
	f.scan(&n, `select count(*) from points.cp_lots where customer_id = $1 and source = $2`, customer, source)
	f.scan(&total, `select coalesce(sum(original), 0) from points.cp_lots where customer_id = $1 and source = $2`,
		customer, source)
	return n, total
}

func (f *cpFixture) available(customer uuid.UUID) int64 {
	f.t.Helper()
	w, err := f.cp.Wallet(f.ctx, customer)
	if err != nil {
		f.t.Fatal(err)
	}
	return w.Available
}

// reconciled checks every lot against its postings.
func (f *cpFixture) reconciled() {
	f.t.Helper()
	r, err := f.cp.Reconcile(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	if len(r.Mismatches) > 0 || r.OrphanedHolds > 0 {
		f.t.Fatalf("reconciliation: %d mismatched lots, %d orphaned holds", len(r.Mismatches), r.OrphanedHolds)
	}
}

// ─── Rewards ─────────────────────────────────────────────────────────────

// A package whose bonus is credited on payment: each paid period puts its
// bonus in the account at once, unused points carry into the next period,
// and a refunded payment takes back what is unused.
func TestCompetitionPointsBonusOnPayment(t *testing.T) {
	f := newCPFixture(t, "NZ")
	o, err := f.cp.CreateOffer(f.ctx, f.adminA, OfferInput{
		TierCode: "pay-" + uuid.NewString()[:6], CountryID: f.country.String(), Name: "Plus",
		Currency: f.currency, PriceMinor: 999, IntervalUnit: "month", IntervalCount: 1,
		MemberBonusPoints: 1000, TermsVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.BonusTrigger != models.BonusTriggerPayment {
		t.Fatalf("the default should be payment, is %q", o.BonusTrigger)
	}
	if o, err = f.cp.OfferTransition(f.ctx, f.adminA, o.ID, "publish"); err != nil {
		t.Fatal(err)
	}
	member := f.customer()

	// Checkout started but not paid: not a member, nothing credited.
	if _, err := f.cp.Join(f.ctx, member, "web", JoinInput{
		OfferID: o.ID, RenewalConsent: true, IdempotencyKey: uuid.NewString()}); err != nil {
		t.Fatalf("join: %v", err)
	}
	if got := f.available(member); got != 0 {
		t.Fatalf("an unpaid checkout credited %d", got)
	}
	st, err := f.cp.Status(f.ctx, member)
	if err != nil || st.State != models.MembershipPaymentPending || st.CurrentPeriod != nil {
		t.Fatalf("unpaid status: %+v %v", st, err)
	}

	// Paid: the bonus is in the account straight away.
	if _, err := f.cp.TestPay(f.ctx, member); err != nil {
		t.Fatalf("pay: %v", err)
	}
	if got := f.available(member); got != 1000 {
		t.Fatalf("after payment: %d, want 1000", got)
	}
	if st, _ = f.cp.Status(f.ctx, member); st.State != models.MembershipBonusUnlocked {
		t.Fatalf("paid status: %s", st.State)
	}
	// Spend 800 of it, then renew: 200 left over plus the new 1000.
	f.exec(`
		with t as (insert into points.cp_transactions (operation_key, kind, customer_id, actor_type)
		           values ('test-spend-' || $1::uuid::text, 'capture', $1::uuid, 'system') returning id),
		     l as (update points.cp_lots set available = available - 800, consumed = consumed + 800
		           where customer_id = $1 returning id)
		insert into points.cp_postings (transaction_id, lot_id, bucket, delta)
		select t.id, l.id, b.bucket, b.delta
		from t, l, (values ('available', -800::bigint), ('consumed', 800::bigint)) b(bucket, delta)`, member)
	if _, err := f.cp.TestPay(f.ctx, member); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if got := f.available(member); got != 1200 {
		t.Fatalf("after renewal: %d, want 1200 (200 carried over + 1000)", got)
	}
	// Paying the same invoice twice credits nothing more.
	if n, total := f.lots(member, models.LotSourceMemberBonus); n != 2 || total != 2000 {
		t.Fatalf("bonus lots %d/%d", n, total)
	}

	// The renewal's payment is refunded: its unused 1000 are taken back.
	var invoice string
	f.scan(&invoice, `select provider_invoice_id from membership.paid_periods
		where customer_id = $1 order by period_start desc limit 1`, member)
	if _, err := f.repo.SetPeriodPaymentStatus(f.ctx, "test", invoice, "refunded"); err != nil {
		t.Fatal(err)
	}
	if got := f.available(member); got != 200 {
		t.Fatalf("after refund: %d, want 200", got)
	}
	f.reconciled()
}

// R01, R02, R03, R04, R08: ordinary points for everyone, one bonus per
// paid period, no points for paying alone, nothing twice.
func TestCompetitionPointsGiftsAndBonus(t *testing.T) {
	f := newCPFixture(t, "NZ")
	f.earning(50)
	offer := f.offer(200)

	// R01: a nonmember earns ordinary points once.
	nonmember := f.customer()
	o := f.gift(nonmember, 5000)
	f.run()
	if n, _ := f.lots(nonmember, models.LotSourceGift); n != 0 {
		t.Fatalf("points before delivery: %d lots", n)
	}
	f.deliver(o)
	f.run()
	f.run()
	if n, total := f.lots(nonmember, models.LotSourceGift); n != 1 || total != 50 {
		t.Fatalf("R01: want 1 lot of 50, got %d lots totalling %d", n, total)
	}
	if n, _ := f.lots(nonmember, models.LotSourceMemberBonus); n != 0 {
		t.Fatalf("R01: a nonmember got a bonus")
	}

	// R02: paying for membership credits nothing; it opens a slot.
	member := f.customer()
	f.join(member, offer)
	f.run()
	if f.available(member) != 0 {
		t.Fatalf("R02: membership payment credited points")
	}
	if got := f.count(`select count(*) from membership.bonus_slots where customer_id = $1 and status = 'unawarded'`, member); got != 1 {
		t.Fatalf("R02: want one unawarded slot, got %d", got)
	}
	st, err := f.cp.Status(f.ctx, member)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != models.MembershipAwaitingGift || st.NextChargeAt == nil || *st.NextChargeMinor != 999 {
		t.Fatalf("R02: status %s, next charge %v", st.State, st.NextChargeAt)
	}

	// R03: the first qualifying gift adds exactly one bonus, as its own lot.
	first := f.gift(member, 3000)
	f.run()
	if st, _ := f.cp.Status(f.ctx, member); st.State != models.MembershipPendingComplete {
		t.Fatalf("R03: want pending_completion, got %s", st.State)
	}
	f.deliver(first)
	f.run()
	if n, total := f.lots(member, models.LotSourceGift); n != 1 || total != 50 {
		t.Fatalf("R03: gift lots %d/%d", n, total)
	}
	if n, total := f.lots(member, models.LotSourceMemberBonus); n != 1 || total != 200 {
		t.Fatalf("R03: bonus lots %d/%d", n, total)
	}

	// R04: a second gift in the same period earns ordinary points only.
	second := f.gift(member, 3000)
	f.deliver(second)
	f.run()
	if n, _ := f.lots(member, models.LotSourceMemberBonus); n != 1 {
		t.Fatalf("R04: a second bonus was awarded")
	}
	if got := f.available(member); got != 300 {
		t.Fatalf("R04: want 300 available, got %d", got)
	}

	// R08: repeated passes and a repeated invoice change nothing.
	sub, err := f.repo.LiveSubscription(f.ctx, member)
	if err != nil || sub == nil {
		t.Fatalf("subscription: %v", err)
	}
	var invoice string
	var start, end time.Time
	f.scan(&invoice, `select provider_invoice_id from membership.paid_periods where customer_id = $1`, member)
	f.scan(&start, `select period_start from membership.paid_periods where customer_id = $1`, member)
	f.scan(&end, `select period_end from membership.paid_periods where customer_id = $1`, member)
	res, err := f.repo.RecordPaidPeriod(f.ctx, repository.PaidInvoice{
		Provider: "test", ProviderSubscriptionID: *sub.ProviderSubscriptionID, InvoiceID: invoice,
		PeriodStart: start, PeriodEnd: end, AmountMinor: 999, Currency: f.currency, PaidAt: time.Now(),
	})
	if err != nil || res.Created {
		t.Fatalf("R08: duplicate invoice: created=%v err=%v", res != nil && res.Created, err)
	}
	f.run()
	if got := f.count(`select count(*) from points.cp_lots where customer_id = $1`, member); got != 3 {
		t.Fatalf("R08: want 3 lots, got %d", got)
	}
	f.reconciled()
}

// R05, R06, R10: a renewal opens a new bonus, only once paid; the payment
// anchor decides the period, and a gift exactly at a boundary belongs to
// the next one.
func TestCompetitionPointsPeriods(t *testing.T) {
	f := newCPFixture(t, "IE")
	f.earning(10)
	offer := f.offer(100)
	member := f.customer()
	f.join(member, offer)
	sub, _ := f.repo.LiveSubscription(f.ctx, member)

	var end time.Time
	f.scan(&end, `select period_end from membership.paid_periods where customer_id = $1`, member)

	// R06: no paid renewal yet, so a gift paid in the next period earns no
	// bonus; R10: paid exactly at the boundary, it belongs to the next.
	atBoundary := f.giftPaidAt(member, 2000, end)
	f.deliver(atBoundary)
	f.run()
	if n, _ := f.lots(member, models.LotSourceMemberBonus); n != 0 {
		t.Fatalf("R06/R10: a bonus without a paid renewal")
	}

	// R05: the renewal is paid (late, after the gift completed): the
	// gift that falls in it unlocks the new period's bonus.
	if _, err := f.repo.RecordPaidPeriod(f.ctx, repository.PaidInvoice{
		Provider: "test", ProviderSubscriptionID: *sub.ProviderSubscriptionID, InvoiceID: "renewal-1",
		PeriodStart: end, PeriodEnd: end.AddDate(0, 1, 0), AmountMinor: 999, Currency: f.currency,
		PaidAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	f.run()
	var slotGift uuid.UUID
	f.scan(&slotGift, `select b.qualifying_gift_id from membership.bonus_slots b
		inner join membership.paid_periods p on p.id = b.paid_period_id
		where p.customer_id = $1 and p.provider_invoice_id = 'renewal-1'`, member)
	var giftOrder uuid.UUID
	f.scan(&giftOrder, `select order_id from points.gift_qualifications where id = $1`, slotGift)
	if giftOrder != atBoundary {
		t.Fatalf("R10: the boundary gift should unlock the renewal's bonus")
	}
	// The first period's slot is still unawarded: the boundary gift did not
	// leak into it.
	if got := f.count(`select count(*) from membership.bonus_slots b
		inner join membership.paid_periods p on p.id = b.paid_period_id
		where p.customer_id = $1 and p.provider_invoice_id <> 'renewal-1' and b.status = 'unawarded'`, member); got != 1 {
		t.Fatalf("R10: first period slot should be unawarded")
	}
	f.reconciled()
}

// R07: two qualifying gifts completing at once both earn ordinary points;
// only one of them unlocks the bonus.
func TestCompetitionPointsConcurrentGifts(t *testing.T) {
	f := newCPFixture(t, "GB")
	f.earning(25)
	offer := f.offer(500)
	member := f.customer()
	f.join(member, offer)
	a, b := f.gift(member, 1000), f.gift(member, 1000)
	f.run()
	f.deliver(a)
	f.deliver(b)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.cp.RunOnce(f.ctx); err != nil {
				t.Errorf("run: %v", err)
			}
		}()
	}
	wg.Wait()
	f.run()
	if n, total := f.lots(member, models.LotSourceGift); n != 2 || total != 50 {
		t.Fatalf("R07: gift lots %d/%d", n, total)
	}
	if n, total := f.lots(member, models.LotSourceMemberBonus); n != 1 || total != 500 {
		t.Fatalf("R07: bonus lots %d/%d", n, total)
	}
	f.reconciled()
}

// R09, R12, R13: refunds take back only the gift's own unused points; spent
// points open a case; a refunded first gift keeps its slot reversed, so a
// later gift gets no automatic second bonus; late membership evidence does
// not reward a refunded gift.
func TestCompetitionPointsRefunds(t *testing.T) {
	f := newCPFixture(t, "NL")
	f.earning(40)
	offer := f.offer(300)

	// R12: refund before award cancels; after award reverses.
	c := f.customer()
	before := f.gift(c, 1000)
	f.run()
	f.refund(before)
	f.run()
	if got := f.count(`select count(*) from points.gift_qualifications g
		inner join marketplace.orders o on o.id = g.order_id
		where o.id = $1 and g.status = 'cancelled'`, before); got != 1 {
		t.Fatalf("R12: refund before award should cancel")
	}
	kept := f.gift(c, 1000)
	after := f.gift(c, 1000)
	f.deliver(kept)
	f.deliver(after)
	f.run()
	if f.available(c) != 80 {
		t.Fatalf("R12: want 80 before the refund, got %d", f.available(c))
	}
	f.refund(after)
	f.run()
	if got := f.available(c); got != 40 {
		t.Fatalf("R12: the refund must take back only that gift's 40, left %d", got)
	}

	// R13: the gift that unlocked the bonus is refunded: both its lots are
	// reversed and the slot stays reversed.
	member := f.customer()
	f.join(member, offer)
	g1 := f.gift(member, 1000)
	f.deliver(g1)
	f.run()
	if f.available(member) != 340 {
		t.Fatalf("R13: want 340, got %d", f.available(member))
	}
	f.refund(g1)
	f.run()
	g2 := f.gift(member, 1000)
	f.deliver(g2)
	f.run()
	if got := f.available(member); got != 40 {
		t.Fatalf("R13: want only the second gift's 40, got %d", got)
	}
	if got := f.count(`select count(*) from membership.bonus_slots where customer_id = $1 and status = 'reversed'`, member); got != 1 {
		t.Fatalf("R13: the slot should be reversed")
	}

	// R09: membership evidence arrives after the gift was refunded: no bonus.
	late := f.customer()
	early := f.gift(late, 1000)
	f.deliver(early)
	f.run()
	f.refund(early)
	f.run()
	f.join(late, offer)
	f.exec(`update membership.paid_periods set period_start = period_start - interval '1 day'
		where customer_id = $1`, late)
	f.run()
	if n, _ := f.lots(late, models.LotSourceMemberBonus); n != 0 {
		t.Fatalf("R09: a refunded gift unlocked a late bonus")
	}
	f.reconciled()
}

// ─── Entries ─────────────────────────────────────────────────────────────

// liveCPRound is a published skill round that costs competition points.
func (f *cpFixture) liveCPRound(cost, maxPlays int) uuid.UUID {
	f.t.Helper()
	rules := "Highest verified score wins."
	start, continueAtCap := int64(1000), true
	view, err := f.comps.CreateCompetition(f.ctx, f.adminA, CompetitionInput{
		CountryIDs: []string{f.country.String()}, GameSlug: "2048", Title: "CP entry test",
		StartsAt: time.Now().Add(time.Hour), EndsAt: time.Now().Add(48 * time.Hour), Timezone: "UTC",
		MaxAttemptsPerCustomer: maxPlays, NumberOfWinners: 1, PrizeDescription: "Cash prize",
		PrizeCurrency: &f.currency, OfficialRules: &rules, StartPrizeCents: &start, ContinueAtCap: &continueAtCap,
		PointsUnit: "competition", EntryCostPoints: &cost,
	})
	if err != nil {
		f.t.Fatalf("create round: %v", err)
	}
	if _, err := f.comps.SetReserve(f.ctx, f.adminA, view.ID, ReserveInput{
		ReserveAmount: start + 1, Currency: f.currency, FundingSource: "sendagift"}); err != nil {
		f.t.Fatalf("reserve: %v", err)
	}
	if _, err := f.comps.FundReserve(f.ctx, f.adminA, view.ID, "escrow #1"); err != nil {
		f.t.Fatalf("fund: %v", err)
	}
	if _, err := f.comps.ScheduleCompetition(f.ctx, f.adminA, view.ID); err != nil {
		f.t.Fatalf("schedule: %v", err)
	}
	f.exec(`update competition.competitions set starts_at = now() - interval '1 minute' where id = $1`, view.ID)
	if err := f.comps.repo.SyncStatuses(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return view.ID
}

func wantCP(t *testing.T, err error, code string) {
	t.Helper()
	var r *CPRefusal
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// S01, E01, E02, E03 and the full reserve-start flow.
func TestCompetitionPointsEntries(t *testing.T) {
	f := newCPFixture(t, "SE")
	f.earning(30)
	round := f.liveCPRound(30, 10)
	player := f.customer()
	g := f.gift(player, 1000)
	f.deliver(g)
	f.run()

	// S01: without an entry approval the server refuses, whatever the client.
	q, err := f.cp.Quote(f.ctx, player, "web", round)
	if err != nil {
		t.Fatal(err)
	}
	if q.CanEnter || q.ReasonCode != models.ReasonLocationUnavailable {
		t.Fatalf("S01: want LOCATION_UNAVAILABLE, got %+v", q)
	}
	_, err = f.cp.Reserve(f.ctx, player, "web", round, ReserveRequest{
		Reference: "forged.ref", ClientRequestID: uuid.NewString()})
	wantCP(t, err, models.ReasonQuoteExpired)

	f.approve(models.OpCompetitionEntry, "web", nil)
	// Approved for the web only: a native app gets CHANNEL_UNAVAILABLE.
	if q, _ := f.cp.Quote(f.ctx, player, "ios", round); q.ReasonCode != models.ReasonChannelUnavailable {
		t.Fatalf("native channel: want CHANNEL_UNAVAILABLE, got %s", q.ReasonCode)
	}
	q, err = f.cp.Quote(f.ctx, player, "web", round)
	if err != nil || !q.CanEnter || q.Cost != 30 || q.EligibleBalance != 30 {
		t.Fatalf("quote: %+v %v", q, err)
	}

	// E02: the legacy play endpoint cannot start a competition-points round.
	_, err = f.comps.Play(f.ctx, round, SocialActor{CustomerID: player.String()}, PlayRequest{ClientRequestID: uuid.NewString()})
	var refusal *PlayRefusal
	if !errors.As(err, &refusal) || refusal.Code != models.ReasonEntryRequired {
		t.Fatalf("E02: want ENTRY_REQUIRED, got %v", err)
	}

	// E01: two reservations race for one entry's worth of points.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.cp.Reserve(f.ctx, player, "web", round, ReserveRequest{
				Reference: q.Reference, ClientRequestID: uuid.NewString()})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		} else {
			wantCP(t, err, models.ReasonInsufficientEligible)
		}
	}
	if ok != 1 {
		t.Fatalf("E01: want exactly one reservation, got %d", ok)
	}
	w, _ := f.cp.Wallet(f.ctx, player)
	if w.Available != 0 || w.Reserved != 30 || len(w.OpenEntries) != 1 {
		t.Fatalf("E01: wallet %+v", w)
	}
	entry := w.OpenEntries[0]

	// A repeated request returns the same entry; the same key for a
	// different request is refused.
	key := "same-key-" + uuid.NewString()[:8]
	g2 := f.gift(player, 1000)
	f.deliver(g2)
	f.run()
	e1, err := f.cp.Reserve(f.ctx, player, "web", round, ReserveRequest{Reference: q.Reference, ClientRequestID: key})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := f.cp.Reserve(f.ctx, player, "web", round, ReserveRequest{Reference: q.Reference, ClientRequestID: key})
	if err != nil || e2.ID != e1.ID || !e2.Replayed {
		t.Fatalf("retry: %v %v", e2, err)
	}
	if _, err := f.cp.CancelEntry(f.ctx, player, e1.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if f.available(player) != 30 {
		t.Fatalf("cancel should return the points")
	}

	// Start: captured once with the attempt.
	view, err := f.cp.StartEntry(f.ctx, player, round, entry.ID, "", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if view.PointsSpent != 30 {
		t.Fatalf("start: points spent %d", view.PointsSpent)
	}
	again, err := f.cp.StartEntry(f.ctx, player, round, entry.ID, "", nil)
	if err != nil || !again.Replayed || again.AttemptID != view.AttemptID {
		t.Fatalf("a repeated start must return the same attempt: %v %v", again, err)
	}
	if _, err := f.cp.CancelEntry(f.ctx, player, entry.ID); err == nil {
		t.Fatalf("a started entry cannot also be released")
	}
	var consumed int64
	f.scan(&consumed, `select coalesce(sum(consumed), 0) from points.cp_lots where customer_id = $1`, player)
	if consumed != 30 {
		t.Fatalf("consumed %d", consumed)
	}
	// Legacy points never moved.
	if got := f.count(`select count(*) from points.points_ledger where customer_id = $1`, player); got != 0 {
		t.Fatalf("legacy points were touched: %d rows", got)
	}
	f.reconciled()
}

// E07: a start and a cancel race: exactly one wins, never both.
func TestCompetitionPointsStartCancelRace(t *testing.T) {
	f := newCPFixture(t, "ZA")
	f.earning(20)
	f.approve(models.OpCompetitionEntry, "web", nil)
	round := f.liveCPRound(20, 20)
	for i := range 5 {
		player := f.customer()
		g := f.gift(player, 1000)
		f.deliver(g)
		f.run()
		q, err := f.cp.Quote(f.ctx, player, "web", round)
		if err != nil || !q.CanEnter {
			t.Fatalf("quote %d: %+v %v", i, q, err)
		}
		e, err := f.cp.Reserve(f.ctx, player, "web", round, ReserveRequest{
			Reference: q.Reference, ClientRequestID: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var startErr, cancelErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, startErr = f.cp.StartEntry(f.ctx, player, round, e.ID, "", nil) }()
		go func() { defer wg.Done(); _, cancelErr = f.cp.CancelEntry(f.ctx, player, e.ID) }()
		wg.Wait()
		if (startErr == nil) == (cancelErr == nil) {
			t.Fatalf("race %d: start=%v cancel=%v: exactly one must win", i, startErr, cancelErr)
		}
		var state string
		f.scan(&state, `select state from competition.cp_entries where id = $1`, e.ID)
		attempts := f.count(`select count(*) from competition.competition_attempts where customer_id = $1`, player)
		if (state == models.EntryCaptured) != (attempts == 1) {
			t.Fatalf("race %d: state %s with %d attempts", i, state, attempts)
		}
	}
	f.reconciled()
}

// Voiding a competition-points play restores the lots it used, with the
// same source and expiry, instead of crediting legacy points.
func TestCompetitionPointsVoidRestores(t *testing.T) {
	f := newCPFixture(t, "SG")
	f.earning(15)
	f.approve(models.OpCompetitionEntry, "web", nil)
	round := f.liveCPRound(15, 5)
	player := f.customer()
	g := f.gift(player, 1000)
	f.deliver(g)
	f.run()
	q, _ := f.cp.Quote(f.ctx, player, "web", round)
	e, err := f.cp.Reserve(f.ctx, player, "web", round, ReserveRequest{Reference: q.Reference, ClientRequestID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.cp.StartEntry(f.ctx, player, round, e.ID, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	refund := true
	if err := f.comps.VoidPlay(f.ctx, f.adminA, round, view.AttemptID, VoidPlayInput{
		Reason: "test void", RefundPoints: &refund}); err != nil {
		t.Fatalf("void: %v", err)
	}
	if got := f.available(player); got != 15 {
		t.Fatalf("want 15 restored, got %d", got)
	}
	if got := f.count(`select count(*) from points.cp_lots where customer_id = $1 and restores_lot_id is not null`, player); got != 1 {
		t.Fatalf("want one restoration lot, got %d", got)
	}
	if got := f.count(`select count(*) from points.points_ledger where customer_id = $1`, player); got != 0 {
		t.Fatalf("legacy points credited on void")
	}
	f.reconciled()
}

// ─── Controls ────────────────────────────────────────────────────────────

// Separation of duties, immutable versions, residence evidence and stops.
func TestCompetitionPointsControls(t *testing.T) {
	f := newCPFixture(t, "MY")

	// The admin who submits cannot approve.
	o, err := f.cp.CreateOffer(f.ctx, f.adminA, OfferInput{
		TierCode: "solo-" + uuid.NewString()[:6], CountryID: f.country.String(), Name: "Solo", Currency: f.currency,
		PriceMinor: 500, IntervalUnit: "day", IntervalCount: 28, MemberBonusPoints: 10, TermsVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if o.BillingLabel != "every 28 days" {
		t.Fatalf("billing label: %q", o.BillingLabel)
	}
	if _, err := f.cp.OfferTransition(f.ctx, f.adminA, o.ID, "submit"); err != nil {
		t.Fatal(err)
	}
	_, err = f.cp.OfferTransition(f.ctx, f.adminA, o.ID, "approve")
	wantCP(t, err, "SECOND_APPROVER_REQUIRED")
	// A submitted version cannot change.
	_, err = f.cp.UpdateOffer(f.ctx, f.adminA, o.ID, OfferInput{
		TierCode: o.TierCode, CountryID: f.country.String(), Name: "Changed", Currency: f.currency,
		PriceMinor: 1, IntervalUnit: "day", IntervalCount: 28, TermsVersion: "v1"})
	wantCP(t, err, "VERSION_LOCKED")
	if _, err := f.pool.Exec(f.ctx, `update membership.plan_offers set price_minor = 1 where id = $1`, o.ID); err == nil {
		t.Fatalf("the database must refuse editing a submitted offer")
	}

	// Approvals need a second admin too.
	expires := time.Now().Add(24 * time.Hour)
	test := "test"
	a, err := f.cp.RequestApproval(f.ctx, f.adminA, ApprovalInput{Operation: models.OpMembershipSale,
		CountryID: f.country.String(), Channel: "web", Provider: &test, EvidenceRef: "REF", ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	// A Super Admin may approve what they recorded themselves; an ordinary
	// admin may not.
	_, err = f.cp.DecideApproval(f.ctx, AdminActor{ID: f.adminA.ID}, a.ID, true)
	wantCP(t, err, "SECOND_APPROVER_REQUIRED")
	if _, err := f.cp.DecideApproval(f.ctx, f.adminA, a.ID, true); err != nil {
		t.Fatal(err)
	}
	var self bool
	f.scan(&self, `select self_approved from core.capability_approvals where id = $1`, a.ID)
	if !self {
		t.Fatalf("a self-approved record should say so")
	}
	// Recorded and approved in one step.
	one, err := f.cp.RequestApproval(f.ctx, f.adminA, ApprovalInput{Operation: models.OpCompetitionEntry,
		CountryID: f.country.String(), Channel: "web", EvidenceRef: "REF2", ExpiresAt: &expires, ApproveNow: true})
	if err != nil || one.Status != "active" || !one.Current {
		t.Fatalf("approve now: %+v %v", one, err)
	}
	if _, err := f.cp.OfferTransition(f.ctx, f.adminB, o.ID, "approve"); err != nil {
		t.Fatal(err)
	}

	// Residence: without a verified local number, a sale needs verification.
	visitor := f.customer()
	f.exec(`update customer.customers set phone_verified_at = null where id = $1`, visitor)
	offers, err := f.cp.Offers(f.ctx, visitor, "web")
	if err != nil {
		t.Fatal(err)
	}
	if offers.Decision.Allowed || offers.Decision.ReasonCode != models.ReasonVerificationRequired {
		t.Fatalf("residence: %+v", offers.Decision)
	}
	// Consent is never assumed.
	resident := f.customer()
	_, err = f.cp.Join(f.ctx, resident, "web", JoinInput{OfferID: o.ID, IdempotencyKey: uuid.NewString()})
	wantCP(t, err, "RENEWAL_CONSENT_REQUIRED")

	// An emergency stop refuses new sales at once; lifting it restores them.
	stop, err := f.cp.ActivateStop(f.ctx, f.adminA, models.StopMembershipSales, nil, "incident drill")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.cp.Join(f.ctx, resident, "web", JoinInput{OfferID: o.ID, RenewalConsent: true, IdempotencyKey: uuid.NewString()})
	wantCP(t, err, models.ReasonOperationStopped)
	if _, err := f.cp.LiftStop(f.ctx, f.adminA, stop.ID, "drill over"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cp.Join(f.ctx, resident, "web", JoinInput{OfferID: o.ID, RenewalConsent: true,
		IdempotencyKey: uuid.NewString()}); err != nil {
		t.Fatalf("join after lift: %v", err)
	}
	// One canonical membership at a time.
	_, err = f.cp.Join(f.ctx, resident, "web", JoinInput{OfferID: o.ID, RenewalConsent: true, IdempotencyKey: uuid.NewString()})
	if err == nil {
		// The unpaid checkout is replaced rather than duplicated.
		if got := f.count(`select count(*) from membership.subscriptions
			where customer_id = $1 and status = 'incomplete'`, resident); got != 1 {
			t.Fatalf("two open memberships")
		}
	}

	// The ledger refuses an unbalanced transaction.
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(f.ctx)
	var lot, txID uuid.UUID
	if err := tx.QueryRow(f.ctx, `insert into points.cp_lots (customer_id, source, source_id, original, available)
		values ($1, 'compensation', gen_random_uuid(), 5, 5) returning id`, resident).Scan(&lot); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(f.ctx, `insert into points.cp_transactions (operation_key, kind, customer_id, actor_type)
		values ($1, 'grant', $2, 'system') returning id`, "test:"+uuid.NewString(), resident).Scan(&txID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(f.ctx, `insert into points.cp_postings (transaction_id, lot_id, bucket, delta)
		values ($1, $2, 'available', 5)`, txID, lot); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err == nil {
		t.Fatalf("an unbalanced transaction committed")
	}
}
