package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// Rewards on credit: a seller short of points still rewards customers, the
// platform fronts the points, and the seller's next purchase pays them back.
// Plus point packages and paying for points on Stripe.

// Runs first in this file: it needs a database with no packages yet.
func TestPointsDefaultPackagesFollowTheRate(t *testing.T) {
	f := newPointsFixture(t, "manual")
	if n, _ := f.repo.CountPackages(f.ctx); n > 0 {
		t.Skip("packages already exist in this database")
	}
	if err := f.sellers.EnsureDefaultPackages(f.ctx); err != nil {
		t.Fatal(err)
	}
	list, err := f.sellers.SellerPackages(f.ctx)
	if err != nil || len(list) != 4 {
		t.Fatalf("starting packages = %d, %v", len(list), err)
	}
	if list[0].PriceCents != 500 || list[0].Points != 50 || list[3].PriceCents != 10000 || list[3].Points != 1000 {
		t.Fatalf("packages at 10c a point: %+v", list)
	}
	// Seeding again changes nothing.
	if err := f.sellers.EnsureDefaultPackages(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := f.repo.CountPackages(f.ctx); n != 4 {
		t.Fatalf("seeded twice: %d packages", n)
	}
}

func TestPointsRewardOnCreditWhenSellerIsShort(t *testing.T) {
	f := newPointsFixture(t, "manual")
	seller := f.seller()
	product := f.product(seller, 250)
	buyer := f.customer("")

	// No points at all, but within the default credit: the customer still
	// gets the reward, and the shortfall is owed.
	o, err := f.order(buyer, product, 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if it := o.Items[0]; it.RewardStatus != "reserved" || it.RewardPoints != 250 {
		t.Fatalf("line on credit: %+v", it)
	}
	w := f.sellerWallet(seller)
	if w.Available != -250 || w.Owed != 250 || w.CreditLeft != repository.DefaultSellerPointsCredit-250 || !w.LowBalance {
		t.Fatalf("after promising on credit: %+v", w)
	}

	// Delivered: the reward is paid in full, out of credit.
	f.deliver(o.ID)
	f.run()
	if b := f.customerBalance(buyer); b != 250 {
		t.Fatalf("customer got %d, want 250", b)
	}
	w = f.sellerWallet(seller)
	if w.Balance != -250 || w.Reserved != 0 || w.Owed != 250 {
		t.Fatalf("after paying out on credit: %+v", w)
	}

	// Past the credit the reward is not promised: 750 left, 1000 needed.
	big := f.product(seller, 1000)
	o2, err := f.order(buyer, big, 1, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if it := o2.Items[0]; it.RewardStatus != "none" {
		t.Fatalf("beyond credit: %+v", it)
	}

	// The next purchase pays off what is owed first.
	p, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{
		Points: 1000, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	done, _, err := f.repo.CompletePurchase(f.ctx, p.ID, repository.PurchaseConfirmation{
		PaidAmountCents: p.AmountCents, ConfirmedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if done.PointsSettled != 250 || *done.PointsCredited != 1000 {
		t.Fatalf("purchase: settled %d of %d, want 250 of 1000", done.PointsSettled, *done.PointsCredited)
	}
	w = f.sellerWallet(seller)
	if w.Balance != 750 || w.Owed != 0 || w.Available != 750 {
		t.Fatalf("after the purchase settled the debt: %+v", w)
	}
}

func TestPointsSellerDebitNeverCreatesDebt(t *testing.T) {
	f := newPointsFixture(t, "manual")
	seller := f.seller()
	f.fund(seller, 100)
	reason := "correction"
	_, err := f.repo.Adjust(f.ctx, repository.PointsChange{
		SellerID: &seller, EntryType: models.PointsEntryAdminDeduction, Delta: -150,
		IdempotencyKey: "test:" + uuid.NewString(), Reason: &reason, ActorType: "admin",
	}, models.AuditEntry{ActorType: "admin", Action: "points.adjusted", EntityType: "seller", EntityID: &seller})
	if !errors.Is(err, repository.ErrPointsInsufficient) {
		t.Fatalf("deducting more than held: err = %v, want insufficient", err)
	}
}

func TestPointsPackagesAndValidation(t *testing.T) {
	f := newPointsFixture(t, "manual")
	admin := AdminActor{ID: uuid.New()}

	bad := []PackageInput{
		{Name: "", Points: 100, PriceCents: 1000},
		{Name: "Too much bonus", Points: 100, BonusPoints: 101, PriceCents: 1000},
		{Name: "Too cheap", Points: 100, PriceCents: 50},
		{Name: "No points", Points: 0, PriceCents: 1000},
	}
	for _, in := range bad {
		if _, err := f.sellers.AdminSavePackage(f.ctx, admin, uuid.Nil, in); !errors.Is(err, ErrInvalidPurchase) {
			t.Errorf("%+v: err = %v, want invalid", in, err)
		}
	}

	name := "Bonus pack " + uuid.NewString()[:6]
	pkg, err := f.sellers.AdminSavePackage(f.ctx, admin, uuid.Nil, PackageInput{
		Name: name, Points: 100, BonusPoints: 20, PriceCents: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Currency != "USD" || !pkg.IsActive || pkg.TotalPoints() != 120 {
		t.Fatalf("saved package: %+v", pkg)
	}
	var audits int
	f.scan(&audits, `select count(*) from admin.audit_log where entity_type = 'point_package' and entity_id = $1`, pkg.ID)
	if audits != 1 {
		t.Fatalf("audit rows = %d, want 1", audits)
	}

	// Bought in full: the bonus is included.
	seller := f.seller()
	p, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{
		PackageID: pkg.ID.String(), IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if p.AmountCents != 1000 || p.Points != 120 || p.BonusPoints != 20 || p.PackageName == nil || *p.PackageName != name {
		t.Fatalf("package purchase: %+v", p)
	}
	done, _, err := f.repo.CompletePurchase(f.ctx, p.ID, repository.PurchaseConfirmation{
		PaidAmountCents: 1000, ConfirmedBy: "admin"})
	if err != nil || *done.PointsCredited != 120 {
		t.Fatalf("credited %v, %v; want 120", done.PointsCredited, err)
	}

	// Paid short: only what the money buys at the rate, no bonus.
	short, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{
		PackageID: pkg.ID.String(), IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	done, _, err = f.repo.CompletePurchase(f.ctx, short.ID, repository.PurchaseConfirmation{
		PaidAmountCents: 500, ConfirmedBy: "admin"})
	if err != nil || *done.PointsCredited != 50 {
		t.Fatalf("short payment credited %v, %v; want 50", done.PointsCredited, err)
	}

	// Switched off: no longer on sale, and cannot be bought.
	off := false
	if _, err := f.sellers.AdminSavePackage(f.ctx, admin, pkg.ID, PackageInput{
		Name: name, Points: 100, BonusPoints: 20, PriceCents: 1000, IsActive: &off}); err != nil {
		t.Fatal(err)
	}
	onSale, err := f.sellers.SellerPackages(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range onSale {
		if p.ID == pkg.ID {
			t.Fatal("a switched-off package is still on sale")
		}
	}
	if _, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{
		PackageID: pkg.ID.String(), IdempotencyKey: uuid.NewString()}); !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("buying a switched-off package: err = %v", err)
	}
}

// Paying for points on Stripe: the purchase opens a payment page, and the
// session being paid credits it exactly once, however many times it is
// reported.
func TestPointsPurchasePaidOnStripe(t *testing.T) {
	f := newPointsFixture(t, "manual")
	var sessions atomic.Int32
	var lastID, lastPurchase string
	stripe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/checkout/sessions":
			_ = r.ParseForm()
			sessions.Add(1)
			lastID = "cs_test_" + uuid.NewString()[:8]
			lastPurchase = r.PostForm.Get("metadata[purchase_id]")
			if r.PostForm.Get("metadata[kind]") != pointsCheckoutKind {
				http.Error(w, "missing kind", 400)
				return
			}
			_, _ = w.Write([]byte(`{"id":"` + lastID + `","url":"https://checkout.stripe.test/` + lastID + `"}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/checkout/sessions/"):
			_, _ = w.Write([]byte(`{"id":"` + lastID + `","status":"complete","payment_status":"paid",
				"amount_total":2000,"currency":"usd","metadata":{"kind":"points_purchase","purchase_id":"` + lastPurchase + `"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer stripe.Close()
	client := NewStripeClient("sk_test_x")
	client.base = stripe.URL
	provider, err := NewStripePointsProvider(client, "http://app")
	if err != nil {
		t.Fatal(err)
	}
	f.sellers = NewSellerPointsService(f.repo, provider, 10, "USD", "")

	seller := f.seller()
	p, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{
		AmountCents: 2000, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != models.PointsPurchasePending || p.CheckoutURL == nil || p.ProviderReference == nil ||
		*p.ProviderReference != lastID || p.Provider != "stripe" {
		t.Fatalf("started purchase: %+v", p)
	}

	// The seller comes back from Stripe: re-checked and credited.
	synced, err := f.sellers.SyncPurchase(f.ctx, seller.String(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if synced.Status != models.PointsPurchaseCompleted || *synced.PointsCredited != 200 {
		t.Fatalf("after sync: %+v", synced)
	}
	// The webhook and the reconcile loop report it again: nothing more.
	cs := StripeCheckoutSession{ID: lastID, PaymentStatus: "paid", AmountTotal: 2000, Currency: "usd",
		Metadata: map[string]string{"kind": pointsCheckoutKind, "purchase_id": p.ID.String()}}
	if err := f.sellers.HandleStripeCheckout(context.Background(), cs); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sellers.ReconcileStripePurchases(f.ctx, 50); err != nil {
		t.Fatal(err)
	}
	if w := f.sellerWallet(seller); w.Balance != 200 || w.LifetimePurchased != 200 {
		t.Fatalf("credited more than once: %+v", w)
	}

	// A lapsed payment page fails its purchase; nothing is credited.
	p2, err := f.sellers.CreatePurchase(f.ctx, seller.String(), PurchaseInput{
		AmountCents: 1000, IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.sellers.HandleStripeCheckout(f.ctx, StripeCheckoutSession{ID: lastID, Status: "expired",
		Metadata: map[string]string{"kind": pointsCheckoutKind, "purchase_id": p2.ID.String()}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.repo.GetPurchase(f.ctx, p2.ID); got.Status != models.PointsPurchaseFailed {
		t.Fatalf("expired purchase status = %q", got.Status)
	}
}
