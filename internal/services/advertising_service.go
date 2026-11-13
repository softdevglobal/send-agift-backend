package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// Advertising packages. An admin creates them: what a package costs in a
// country, how many products it promotes, and what a customer earns in game
// entries for buying the product in each priority level. A seller buys one
// through Stripe and puts products in its levels. The package ends when its
// entries are used up or its time runs out, whichever comes first.

var (
	ErrInvalidAdPackage   = errors.New("invalid advertising package")
	ErrAdPackageNotFound  = errors.New("advertising package not found")
	ErrAdPackageState     = errors.New("that is not possible for a package in this state")
	ErrInvalidAdPurchase  = errors.New("invalid advertising purchase")
	ErrAdPurchaseNotFound = errors.New("advertising purchase not found")
	ErrAdPurchaseState    = errors.New("that is not possible for this purchase now")
	// ErrAdTestPaymentsOff is confirming a payment by hand outside a sandbox.
	ErrAdTestPaymentsOff = errors.New("test payments are not enabled")
)

const adCheckoutKind = "ad_package_purchase"

func invalidAd(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidAdPackage, msg) }

func mapAdErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repository.ErrAdPackageNotFound):
		return ErrAdPackageNotFound
	case errors.Is(err, repository.ErrAdPackageState):
		return ErrAdPackageState
	case errors.Is(err, repository.ErrAdPurchaseNotFound):
		return ErrAdPurchaseNotFound
	case errors.Is(err, repository.ErrAdPurchaseState):
		return ErrAdPurchaseState
	case errors.Is(err, repository.ErrAdPaymentMismatch),
		errors.Is(err, repository.ErrAdProductTaken),
		errors.Is(err, repository.ErrAdProductInvalid),
		errors.Is(err, repository.ErrAdLevelInvalid):
		return fmt.Errorf("%w: %s", ErrInvalidAdPurchase, err.Error())
	}
	return err
}

// AdvertisingService sells advertising packages to sellers.
type AdvertisingService struct {
	repo    *repository.AdRepository
	sellers *repository.SellerRepository // nil skips the email-change hold
	stripe  *StripeClient                // nil: no card provider connected
	emails  *EmailService
	webURL  string
	// testPayments lets a sandbox seller confirm their own payment when no
	// Stripe key is set. Never on in production.
	testPayments bool
}

func NewAdvertisingService(repo *repository.AdRepository, sellers *repository.SellerRepository, stripe *StripeClient, emails *EmailService, webURL string, testPayments bool) *AdvertisingService {
	return &AdvertisingService{
		repo: repo, sellers: sellers, stripe: stripe, emails: emails,
		webURL: strings.TrimRight(webURL, "/"), testPayments: testPayments && stripe == nil,
	}
}

// provider names how purchases are paid: Stripe, a sandbox's test
// payments, or by hand (an admin confirms a payment they have seen).
func (s *AdvertisingService) provider() string {
	switch {
	case s.stripe != nil:
		return "stripe"
	case s.testPayments:
		return "test"
	}
	return "manual"
}

func parseSeller(sellerID string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(sellerID))
	if err != nil {
		return uuid.Nil, ErrAdPurchaseNotFound
	}
	return id, nil
}

// ─── Admin: packages ──────────────────────────────────────────────────────

// AdPackageInput is a package as an admin writes it. LevelEntries lists
// what a customer earns per purchase in each priority level, top level
// first; its length is the number of products the package promotes.
type AdPackageInput struct {
	CountryID    string  `json:"country_id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	Currency     string  `json:"currency"`
	PriceMinor   int64   `json:"price_minor"`
	LevelEntries []int   `json:"level_entries"`
	EntriesPool  int     `json:"entries_pool"`
	ValidityDays int     `json:"validity_days"`
}

func (in AdPackageInput) toModel() (*models.AdPackage, error) {
	country, err := uuid.Parse(strings.TrimSpace(in.CountryID))
	if err != nil {
		return nil, invalidAd("country_id is required")
	}
	p := &models.AdPackage{
		CountryID: country, Name: strings.TrimSpace(in.Name),
		Currency:   strings.ToUpper(strings.TrimSpace(in.Currency)),
		PriceMinor: in.PriceMinor, ProductSlots: len(in.LevelEntries),
		EntriesPool: in.EntriesPool, ValidityDays: in.ValidityDays,
	}
	if in.Description != nil {
		if d := strings.TrimSpace(*in.Description); d != "" {
			p.Description = &d
		}
	}
	switch {
	case p.Name == "" || len(p.Name) > 80:
		return nil, invalidAd("name must be 1 to 80 characters")
	case p.Description != nil && len(*p.Description) > 600:
		return nil, invalidAd("description must be at most 600 characters")
	case !currencyRe.MatchString(p.Currency):
		return nil, invalidAd("currency must be a 3-letter ISO code")
	case p.PriceMinor <= 0:
		return nil, invalidAd("price_minor must be positive")
	case p.ProductSlots < 1 || p.ProductSlots > 20:
		return nil, invalidAd("a package promotes 1 to 20 products: give level_entries one number per product")
	case p.ValidityDays < 1 || p.ValidityDays > 365:
		return nil, invalidAd("validity_days must be 1 to 365")
	}
	top := 0
	for i, n := range in.LevelEntries {
		if n < 1 || n > 1000 {
			return nil, invalidAd("each level must give 1 to 1000 game entries")
		}
		if i > 0 && n > in.LevelEntries[i-1] {
			return nil, invalidAd("a lower priority level cannot give more entries than the level above it")
		}
		if n > top {
			top = n
		}
		p.LevelEntries = append(p.LevelEntries, int32(n))
	}
	if p.EntriesPool < top {
		return nil, invalidAd(fmt.Sprintf("entries_pool must be at least %d, enough for one purchase at the top level", top))
	}
	if p.EntriesPool > 10_000_000 {
		return nil, invalidAd("entries_pool is too large")
	}
	return p, nil
}

func (a AdminActor) adAudit(action string, id *uuid.UUID, reason *string) models.AuditEntry {
	entry := a.cpAudit(action, "ad_package")
	entry.EntityID, entry.Reason = id, reason
	return entry
}

func (s *AdvertisingService) AdminPackages(ctx context.Context) ([]models.AdPackage, error) {
	return s.repo.ListPackages(ctx)
}

func (s *AdvertisingService) CreatePackage(ctx context.Context, admin AdminActor, in AdPackageInput) (*models.AdPackage, error) {
	p, err := in.toModel()
	if err != nil {
		return nil, err
	}
	audit := admin.adAudit("ads.package_created", nil, nil)
	audit.After = p
	out, err := s.repo.SavePackageDraft(ctx, nil, p, admin.ID, audit)
	return out, mapAdErr(err)
}

// UpdatePackage rewrites a draft. A published package is changed by making
// a new version of it (PackageAction "new-version"), which is then edited
// and published in its place.
func (s *AdvertisingService) UpdatePackage(ctx context.Context, admin AdminActor, id uuid.UUID, in AdPackageInput) (*models.AdPackage, error) {
	p, err := in.toModel()
	if err != nil {
		return nil, err
	}
	audit := admin.adAudit("ads.package_updated", &id, nil)
	audit.After = p
	out, err := s.repo.SavePackageDraft(ctx, &id, p, admin.ID, audit)
	return out, mapAdErr(err)
}

// PackageAction publishes a draft, retires a published package, or starts
// a new version of one.
func (s *AdvertisingService) PackageAction(ctx context.Context, admin AdminActor, id uuid.UUID, action string) (*models.AdPackage, error) {
	var (
		out *models.AdPackage
		err error
	)
	switch action {
	case "publish":
		out, err = s.repo.PublishPackage(ctx, id, admin.ID, admin.adAudit("ads.package_published", &id, nil))
	case "retire":
		out, err = s.repo.RetirePackage(ctx, id, admin.ID, admin.adAudit("ads.package_retired", &id, nil))
	case "new-version":
		out, err = s.repo.NewPackageVersion(ctx, id, admin.ID, admin.adAudit("ads.package_version_created", &id, nil))
	default:
		return nil, invalidAd("action must be publish, retire or new-version")
	}
	return out, mapAdErr(err)
}

func (s *AdvertisingService) DeletePackage(ctx context.Context, admin AdminActor, id uuid.UUID) error {
	return mapAdErr(s.repo.DeletePackageDraft(ctx, id, admin.adAudit("ads.package_deleted", &id, nil)))
}

// ─── Admin: purchases and reminders ───────────────────────────────────────

// AdminPurchases lists sellers' purchases with their budget use.
func (s *AdvertisingService) AdminPurchases(ctx context.Context, status string) ([]models.AdPurchase, error) {
	status = strings.TrimSpace(status)
	switch status {
	case "", models.AdPurchasePending, models.AdPurchaseActive, models.AdPurchaseExhausted,
		models.AdPurchaseExpired, models.AdPurchaseCancelled, models.AdPurchaseFailed, models.AdPurchaseRefunded:
	default:
		return nil, fmt.Errorf("%w: unknown status", ErrInvalidAdPurchase)
	}
	return s.repo.AdminPurchases(ctx, status, 300)
}

// AdminConfirmPurchase activates a purchase whose payment an admin has seen
// arrive outside the card provider, audited. The amount confirmed is the
// package's price.
func (s *AdvertisingService) AdminConfirmPurchase(ctx context.Context, admin AdminActor, id uuid.UUID, reference string) (*models.AdPurchase, error) {
	p, err := s.repo.GetPurchase(ctx, id)
	if err != nil {
		return nil, mapAdErr(err)
	}
	if p.Status != models.AdPurchasePending {
		return nil, ErrAdPurchaseState
	}
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return nil, fmt.Errorf("%w: a payment reference is required", ErrInvalidAdPurchase)
	}
	ref := "admin:" + reference
	return s.activate(ctx, id, repository.AdPayment{AmountMinor: p.PriceMinor, Currency: p.Currency, Reference: &ref})
}

// ResendReminder sends a seller their current budget notice again.
func (s *AdvertisingService) ResendReminder(ctx context.Context, admin AdminActor, purchaseID uuid.UUID) (*models.AdReminder, error) {
	audit := admin.adAudit("ads.reminder_resent", &purchaseID, nil)
	audit.EntityType = "ad_purchase"
	rem, err := s.repo.ResendReminder(ctx, purchaseID, admin.ID, audit)
	if errors.Is(err, repository.ErrAdPurchaseState) {
		return nil, fmt.Errorf("%w: this package has used less than 80%% of its budget, so there is no reminder to send", ErrInvalidAdPurchase)
	}
	if err != nil {
		return nil, mapAdErr(err)
	}
	s.sendReminders(ctx)
	return rem, nil
}

// ─── Seller ───────────────────────────────────────────────────────────────

// Dashboard is the seller's advertising page: their packages with the
// promoted products and budget use, their notices, and what they can buy.
func (s *AdvertisingService) Dashboard(ctx context.Context, sellerID string) (*models.SellerAdvertising, error) {
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	out := &models.SellerAdvertising{}
	if out.Purchases, err = s.repo.SellerPurchases(ctx, id, 100); err != nil {
		return nil, err
	}
	if out.Reminders, err = s.repo.SellerReminders(ctx, id, 50); err != nil {
		return nil, err
	}
	country, err := s.repo.SellerCountry(ctx, id)
	if err != nil {
		return nil, mapAdErr(err)
	}
	if out.Packages, err = s.repo.PublishedPackages(ctx, country); err != nil {
		return nil, err
	}
	return out, nil
}

// MarkRemindersRead records that the seller has seen their notices.
func (s *AdvertisingService) MarkRemindersRead(ctx context.Context, sellerID string) error {
	id, err := parseSeller(sellerID)
	if err != nil {
		return err
	}
	return s.repo.MarkRemindersRead(ctx, id)
}

// BuyInput starts a purchase. The key makes a repeated request the same
// purchase, not a second one.
type BuyInput struct {
	PackageID      string `json:"package_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Buy starts a seller's purchase of a package on sale in their country. It
// is pending, with a Stripe payment page to go to, until the payment is
// confirmed. Buying is never automatic: every purchase is one the seller
// asked for.
func (s *AdvertisingService) Buy(ctx context.Context, sellerID string, in BuyInput) (*models.AdPurchase, error) {
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	if s.sellers != nil {
		if err := ensureFundingOpen(ctx, s.sellers, id); err != nil {
			return nil, err
		}
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" || len(key) > 128 {
		return nil, fmt.Errorf("%w: idempotency_key of 1 to 128 characters is required", ErrInvalidAdPurchase)
	}
	pkgID, err := uuid.Parse(strings.TrimSpace(in.PackageID))
	if err != nil {
		return nil, ErrAdPackageNotFound
	}
	pkg, err := s.repo.GetPackage(ctx, pkgID)
	if err != nil {
		return nil, mapAdErr(err)
	}
	country, err := s.repo.SellerCountry(ctx, id)
	if err != nil {
		return nil, mapAdErr(err)
	}
	if pkg.Status != models.AdPackagePublished || pkg.CountryID != country {
		return nil, fmt.Errorf("%w: this package is not on sale in your country", ErrInvalidAdPurchase)
	}
	p, err := s.repo.CreatePurchase(ctx, id, pkg, s.provider(), key)
	if err != nil {
		return nil, mapAdErr(err)
	}
	if p.Status != models.AdPurchasePending || p.CheckoutURL != nil || s.stripe == nil {
		return p, nil
	}
	back := s.webURL + "/seller/advertising?purchase=" + p.ID.String()
	cs, err := s.stripe.CreateCheckoutSession(ctx, CheckoutParams{
		Lines: []CheckoutLine{{
			Name:       fmt.Sprintf("%s advertising package: %d products, %d days", p.PackageName, p.ProductSlots, p.ValidityDays),
			UnitAmount: int(p.PriceMinor), Quantity: 1,
		}},
		Amount:        int(p.PriceMinor),
		Currency:      p.Currency,
		OrderNumber:   "advertising",
		CustomerEmail: p.SellerEmail,
		SuccessURL:    back + "&paid=1",
		CancelURL:     back + "&cancelled=1",
		Metadata: map[string]string{
			"kind":        adCheckoutKind,
			"purchase_id": p.ID.String(),
			"seller_id":   p.SellerID.String(),
		},
	}, "ad-pay-"+p.ID.String())
	if err != nil {
		return nil, err
	}
	if cs.URL == "" {
		return nil, fmt.Errorf("stripe returned no checkout url for advertising purchase %s", p.ID)
	}
	if err := s.repo.SetPurchaseCheckout(ctx, p.ID, &cs.URL, &cs.ID); err != nil {
		return nil, err
	}
	return s.repo.GetPurchase(ctx, p.ID)
}

// Cancel abandons one of the seller's pending purchases.
func (s *AdvertisingService) Cancel(ctx context.Context, sellerID string, purchaseID uuid.UUID) (*models.AdPurchase, error) {
	p, err := s.sellerPurchase(ctx, sellerID, purchaseID)
	if err != nil {
		return nil, err
	}
	out, err := s.repo.EndPurchase(ctx, p.ID, models.AdPurchaseCancelled, "Cancelled by the seller")
	return out, mapAdErr(err)
}

func (s *AdvertisingService) sellerPurchase(ctx context.Context, sellerID string, purchaseID uuid.UUID) (*models.AdPurchase, error) {
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	p, err := s.repo.GetSellerPurchase(ctx, id, purchaseID)
	return p, mapAdErr(err)
}

// TestPay confirms a seller's own purchase in a sandbox with no card
// provider connected.
func (s *AdvertisingService) TestPay(ctx context.Context, sellerID string, purchaseID uuid.UUID) (*models.AdPurchase, error) {
	if !s.testPayments {
		return nil, ErrAdTestPaymentsOff
	}
	p, err := s.sellerPurchase(ctx, sellerID, purchaseID)
	if err != nil {
		return nil, err
	}
	ref := "test-" + p.ID.String()
	return s.activate(ctx, p.ID, repository.AdPayment{AmountMinor: p.PriceMinor, Currency: p.Currency, Reference: &ref})
}

// Sync re-reads a pending purchase from Stripe, for a seller back from the
// payment page before the webhook has arrived.
func (s *AdvertisingService) Sync(ctx context.Context, sellerID string, purchaseID uuid.UUID) (*models.AdPurchase, error) {
	p, err := s.sellerPurchase(ctx, sellerID, purchaseID)
	if err != nil {
		return nil, err
	}
	if err := s.recheck(ctx, p); err != nil {
		log.Printf("advertising: re-check purchase %s: %v", p.ID, err)
	}
	return s.sellerPurchase(ctx, sellerID, purchaseID)
}

// PromotionInput puts a product in a priority level (1 is the top).
type PromotionInput struct {
	ProductID string `json:"product_id"`
	Level     int    `json:"level"`
}

// SetPromotions sets which of the seller's products the package promotes
// and in which levels. Sending the list again in a new order is how the
// seller reorders it while the package is active.
func (s *AdvertisingService) SetPromotions(ctx context.Context, sellerID string, purchaseID uuid.UUID, in []PromotionInput) (*models.AdPurchase, error) {
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	slots := make([]repository.PromotionSlot, 0, len(in))
	for _, row := range in {
		pid, err := uuid.Parse(strings.TrimSpace(row.ProductID))
		if err != nil {
			return nil, fmt.Errorf("%w: product_id must be a product ID", ErrInvalidAdPurchase)
		}
		slots = append(slots, repository.PromotionSlot{ProductID: pid, Level: row.Level})
	}
	sort.Slice(slots, func(a, b int) bool { return slots[a].Level < slots[b].Level })
	out, err := s.repo.SetPromotions(ctx, id, purchaseID, slots)
	if errors.Is(err, repository.ErrAdPurchaseState) {
		return nil, fmt.Errorf("%w: this package is no longer active, so its list cannot be changed", ErrAdPurchaseState)
	}
	return out, mapAdErr(err)
}

// ─── Payment ──────────────────────────────────────────────────────────────

// activate turns a confirmed payment into an active package, once, and
// emails the seller their receipt.
func (s *AdvertisingService) activate(ctx context.Context, id uuid.UUID, pay repository.AdPayment) (*models.AdPurchase, error) {
	p, activated, err := s.repo.CompletePurchase(ctx, id, pay)
	if err != nil {
		return nil, mapAdErr(err)
	}
	if activated {
		log.Printf("advertising: purchase %s paid (%d %s)", id, pay.AmountMinor, strings.ToUpper(pay.Currency))
		if err := s.emails.SendAdPurchaseReceipt(ctx, p); err != nil {
			log.Printf("advertising: receipt email for %s: %v", id, err)
		}
	}
	return p, nil
}

// HandleStripeCheckout applies a Checkout Session event for an advertising
// purchase. The package is activated only for a paid session whose amount
// and currency are the purchase's own and whose metadata names this seller:
// the redirect back from the payment page proves nothing. A session that
// expired unpaid fails the purchase. A repeated event changes nothing.
func (s *AdvertisingService) HandleStripeCheckout(ctx context.Context, cs StripeCheckoutSession) error {
	id, err := uuid.Parse(cs.Metadata["purchase_id"])
	if err != nil {
		return nil // not one of ours
	}
	p, err := s.repo.GetPurchase(ctx, id)
	if errors.Is(err, repository.ErrAdPurchaseNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if p.Status != models.AdPurchasePending {
		return nil
	}
	if seller := cs.Metadata["seller_id"]; seller != "" && seller != p.SellerID.String() {
		return fmt.Errorf("advertising: session %s names seller %s, purchase %s belongs to %s", cs.ID, seller, p.ID, p.SellerID)
	}
	if p.ProviderReference != nil && cs.ID != "" && *p.ProviderReference != cs.ID {
		return fmt.Errorf("advertising: session %s is not the one started for purchase %s", cs.ID, p.ID)
	}
	switch {
	case cs.PaymentStatus == "paid":
		ref := cs.ID
		_, err := s.activate(ctx, id, repository.AdPayment{
			AmountMinor: cs.AmountTotal, Currency: cs.Currency, Reference: &ref,
		})
		return err
	case cs.Status == "expired":
		_, err := s.repo.EndPurchase(ctx, id, models.AdPurchaseFailed, "The payment page expired before it was paid")
		if err != nil && !errors.Is(err, repository.ErrAdPurchaseState) {
			return err
		}
	}
	return nil
}

func (s *AdvertisingService) recheck(ctx context.Context, p *models.AdPurchase) error {
	if s.stripe == nil || p.Status != models.AdPurchasePending || p.ProviderReference == nil ||
		!strings.HasPrefix(*p.ProviderReference, "cs_") {
		return nil
	}
	cs, err := s.stripe.GetCheckoutSession(ctx, *p.ProviderReference)
	if err != nil {
		return err
	}
	if cs.Metadata == nil {
		cs.Metadata = map[string]string{}
	}
	cs.Metadata["purchase_id"] = p.ID.String()
	return s.HandleStripeCheckout(ctx, *cs)
}

// ─── Loops ────────────────────────────────────────────────────────────────

// sendReminders emails the budget notices not yet sent.
func (s *AdvertisingService) sendReminders(ctx context.Context) {
	list, err := s.repo.UnsentReminders(ctx, 100)
	if err != nil {
		log.Printf("advertising: reminders: %v", err)
		return
	}
	for i := range list {
		if err := s.emails.SendAdReminder(ctx, &list[i], s.webURL+"/seller/advertising"); err != nil {
			log.Printf("advertising: reminder %s: %v", list[i].ID, err)
			continue
		}
		if err := s.repo.MarkReminderEmailed(ctx, list[i].ID); err != nil {
			log.Printf("advertising: reminder %s: %v", list[i].ID, err)
		}
	}
}

// RunLoop emails budget reminders and re-checks pending Stripe purchases
// (a webhook that never arrived), on one server at a time.
func (s *AdvertisingService) RunLoop(ctx context.Context, every time.Duration, exclusive Exclusive) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := exclusive.run(ctx, func(ctx context.Context) error {
				s.sendReminders(ctx)
				if s.stripe == nil {
					return nil
				}
				pending, err := s.repo.PendingPurchases(ctx, "stripe", 50)
				if err != nil {
					return err
				}
				for i := range pending {
					if err := s.recheck(ctx, &pending[i]); err != nil {
						log.Printf("advertising: reconcile purchase %s: %v", pending[i].ID, err)
					}
				}
				return nil
			}); err != nil {
				log.Printf("advertising loop: %v", err)
			}
		}
	}
}
