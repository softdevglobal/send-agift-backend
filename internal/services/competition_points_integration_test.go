package services

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"os"
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
		adminA: AdminActor{ID: uuid.New()}, adminB: AdminActor{ID: uuid.New()}}
	f.scan(&f.country, `
		insert into core.countries (iso_code, name, default_currency, default_timezone)
		values ($1, $2, $3, 'UTC')
		on conflict (iso_code) do update set name = excluded.name returning id`, iso, "CP "+iso, c.currency)
	f.exec(`
		insert into core.country_capabilities
			(country_id, skill_competitions_enabled, progressive_prizes_enabled, entries_usage_enabled,
			 entries_earning_enabled, chance_games_enabled)
		values ($1, true, true, true, true, false)
		on conflict (country_id) do update set skill_competitions_enabled = true,
			progressive_prizes_enabled = true, entries_usage_enabled = true`, f.country)
	// Approvals and stops from an earlier run of a test would leak into
	// this one.
	f.exec(`update core.capability_approvals set status = 'revoked', revoked_at = now(),
		revoke_reason = 'test reset' where country_id = $1 and status in ('pending', 'active')`, f.country)
	f.exec(`update core.emergency_stops set lifted_at = now(), lifted_by_admin_id = $2, lift_reason = 'test reset'
		where lifted_at is null and (country_id = $1 or country_id is null)`, f.country, f.adminA.ID)

	f.repo = repository.NewCompetitionPointsRepository(pool)
	pointsRepo := repository.NewGameEntriesRepository(pool)
	f.comps = NewCompetitionService(repository.NewCompetitionRepository(pool), repository.NewGameRepository(pool),
		repository.NewCustomerRepository(pool), repository.NewCountryCapabilityRepository(pool),
		repository.NewCountryRepository(pool), pointsRepo)
	f.cp = NewCompetitionPointsService(f.repo, f.comps, nil, CompetitionPointsConfig{
		WebURL: "http://localhost:5173", TestPayments: true,
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

// offer publishes a monthly membership with the given extra bonus.
func (f *cpFixture) offer() *models.PlanOffer {
	f.t.Helper()
	o, err := f.cp.CreateOffer(f.ctx, f.adminA, OfferInput{
		TierCode: "plus-" + uuid.NewString()[:6], CountryID: f.country.String(), Name: "Plus",
		Currency: f.currency, PriceMinor: 999, IntervalUnit: "month", IntervalCount: 1,
		TermsVersion: "v1",
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

// ─── Rewards ─────────────────────────────────────────────────────────────

// ─── Entries ─────────────────────────────────────────────────────────────

func wantCP(t *testing.T, err error, code string) {
	t.Helper()
	var r *CPRefusal
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// ─── Controls ────────────────────────────────────────────────────────────

// Separation of duties, immutable versions, residence evidence and stops.
func TestCompetitionPointsControls(t *testing.T) {
	f := newCPFixture(t, "MY")

	// The admin who submits cannot approve.
	o, err := f.cp.CreateOffer(f.ctx, f.adminA, OfferInput{
		TierCode: "solo-" + uuid.NewString()[:6], CountryID: f.country.String(), Name: "Solo", Currency: f.currency,
		PriceMinor: 500, IntervalUnit: "day", IntervalCount: 28, TermsVersion: "v1"})
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

	// Approvals.
	expires := time.Now().Add(24 * time.Hour)
	test := "test"
	a, err := f.cp.RequestApproval(f.ctx, f.adminA, ApprovalInput{Operation: models.OpMembershipSale,
		CountryID: f.country.String(), Channel: "web", Provider: &test, EvidenceRef: "REF", ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	// An admin may approve what they recorded themselves.
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
}

// The admin members list shows who is a member and on which package: a paid
// membership appears, an unpaid checkout does not, and ended ones are kept
// apart from current ones.
func TestAdminMembersList(t *testing.T) {
	f := newCPFixture(t, "NZ")
	o := f.offer()
	member, unpaid := f.customer(), f.customer()
	f.join(member, o)
	if _, err := f.cp.Join(f.ctx, unpaid, "web", JoinInput{
		OfferID: o.ID, RenewalConsent: true, IdempotencyKey: uuid.NewString()}); err != nil {
		t.Fatalf("join: %v", err)
	}

	list, err := f.cp.AdminMembers(f.ctx, repository.MemberFilter{TierCode: o.TierCode})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].CustomerID != member {
		t.Fatalf("current members: total %d, items %+v", list.Total, list.Items)
	}
	m := list.Items[0]
	if m.PackageName != "Plus" || m.TierCode != o.TierCode || m.Status != "active" || m.PeriodsPaid != 1 ||
		m.TotalPaid != 999 || m.PeriodEnd == nil || m.BillingLabel == "" || m.Email == "" {
		t.Fatalf("member row: %+v", m)
	}
	var onPackage int
	for _, p := range list.Packages {
		if p.TierCode == o.TierCode {
			onPackage = p.Members
		}
	}
	if onPackage != 1 {
		t.Fatalf("package counts: %+v", list.Packages)
	}
	if byName, _ := f.cp.AdminMembers(f.ctx, repository.MemberFilter{Query: "no-such-person"}); byName == nil || byName.Total != 0 {
		t.Fatalf("search should find nobody: %+v", byName)
	}

	// Ended memberships are listed under "ended", not "current".
	f.exec(`update membership.subscriptions set status = 'cancelled' where customer_id = $1`, member)
	if cur, _ := f.cp.AdminMembers(f.ctx, repository.MemberFilter{TierCode: o.TierCode}); cur.Total != 0 {
		t.Fatalf("a cancelled membership is not current: %+v", cur.Items)
	}
	if ended, _ := f.cp.AdminMembers(f.ctx, repository.MemberFilter{State: "ended", TierCode: o.TierCode}); ended.Total != 1 {
		t.Fatalf("ended: %+v", ended)
	}
	if _, err := f.cp.AdminMembers(f.ctx, repository.MemberFilter{State: "bogus"}); err == nil {
		t.Fatalf("an unknown state must be refused")
	}
}

// A member who cancelled can turn renewal back on before the period ends.
func TestMembershipResumeRenewal(t *testing.T) {
	f := newCPFixture(t, "NZ")
	o := f.offer()
	member := f.customer()
	f.join(member, o)
	st, err := f.cp.Cancel(f.ctx, member)
	if err != nil || st.Subscription == nil || !st.Subscription.CancelAtPeriodEnd || st.NextChargeAt != nil {
		t.Fatalf("after cancel: %+v %v", st, err)
	}
	st, err = f.cp.Resume(f.ctx, member, "web")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if st.Subscription.CancelAtPeriodEnd || st.Subscription.Status != models.SubscriptionActive || st.NextChargeAt == nil {
		t.Fatalf("after resume: %+v", st.Subscription)
	}
	_, err = f.cp.Resume(f.ctx, member, "web")
	wantCP(t, err, "NOTHING_TO_RESUME")
}
