package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrInvalidPurchase  = errors.New("invalid points purchase")
	ErrPurchaseNotFound = errors.New("points purchase not found")
	ErrPurchaseState    = errors.New("points purchase is no longer pending")
	// ErrTestPaymentsOff is a seller trying to complete their own purchase
	// when the provider is not the test one.
	ErrTestPaymentsOff = errors.New("test payments are not enabled; an admin confirms payments")
	// ErrWebhookNotConfigured is a provider confirmation with no secret set
	// to check it against.
	ErrWebhookNotConfigured = errors.New("points payment webhook is not configured")
	ErrWebhookSignature     = errors.New("webhook signature is not valid")
)

// Limits on one purchase, in minor units: $1 to $10,000 at the default rate.
const (
	minPurchaseCents = 100
	maxPurchaseCents = 1_000_000
)

// PointsPaymentProvider takes the payment for a seller's points purchase.
// It only ever starts a payment: the purchase is credited when the payment
// is confirmed — by the provider's signed webhook, or by an admin — never on
// the seller's word.
//
// A real card provider (Stripe, PayHere, ...) is a new implementation of
// this interface plus its webhook mapping; the points logic does not change.
type PointsPaymentProvider interface {
	Name() string
	// StartCheckout begins paying for a purchase and returns where to send
	// the seller and the provider's reference for the payment, when it has
	// them.
	StartCheckout(ctx context.Context, p *models.PointsPurchase) (url, reference *string, err error)
	// SelfConfirm is whether the seller may complete their own payment,
	// which only the test provider allows.
	SelfConfirm() bool
}

// manualPayments has no checkout: the seller pays by arrangement (e.g. a
// bank transfer) and an admin confirms it.
type manualPayments struct{}

func (manualPayments) Name() string { return "manual" }
func (manualPayments) StartCheckout(context.Context, *models.PointsPurchase) (*string, *string, error) {
	return nil, nil, nil
}
func (manualPayments) SelfConfirm() bool { return false }

// testPayments lets the seller approve or decline their own payment, for
// development. Never enable it where the points are worth anything.
type testPayments struct{}

func (testPayments) Name() string { return "test" }
func (testPayments) StartCheckout(context.Context, *models.PointsPurchase) (*string, *string, error) {
	return nil, nil, nil
}
func (testPayments) SelfConfirm() bool { return true }

// instantPayments credits a purchase the moment it is made, with no payment
// taken. It is the stand-in until a card provider (Stripe) is connected:
// swap POINTS_PAYMENT_PROVIDER to that provider and purchases go back to
// waiting for a confirmed payment.
type instantPayments struct{}

func (instantPayments) Name() string { return "instant" }
func (instantPayments) StartCheckout(context.Context, *models.PointsPurchase) (*string, *string, error) {
	return nil, nil, nil
}
func (instantPayments) SelfConfirm() bool { return true }

// completesInstantly is whether a provider credits a purchase as soon as it
// is created.
func completesInstantly(p PointsPaymentProvider) bool {
	_, ok := p.(instantPayments)
	return ok
}

// NewPointsPaymentProvider returns the provider configured by name.
func NewPointsPaymentProvider(name string) (PointsPaymentProvider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "manual":
		return manualPayments{}, nil
	case "test":
		return testPayments{}, nil
	case "instant":
		return instantPayments{}, nil
	default:
		return nil, fmt.Errorf("unknown POINTS_PAYMENT_PROVIDER %q (use instant, manual or test)", name)
	}
}

// SellerPointsService is sellers buying points, and the admin tools and
// provider webhook that confirm those purchases.
type SellerPointsService struct {
	points        *repository.PointsRepository
	provider      PointsPaymentProvider
	rate          models.PointsRate
	webhookSecret []byte
}

func NewSellerPointsService(points *repository.PointsRepository, provider PointsPaymentProvider, centsPerPoint int, currency, webhookSecret string) *SellerPointsService {
	if centsPerPoint <= 0 {
		centsPerPoint = 10
	}
	return &SellerPointsService{
		points:        points,
		provider:      provider,
		rate:          models.PointsRate{CentsPerPoint: centsPerPoint, Currency: strings.ToUpper(currency)},
		webhookSecret: []byte(webhookSecret),
	}
}

// Rate is what one point costs.
func (s *SellerPointsService) Rate() models.PointsRate { return s.rate }

func parseSeller(sellerID string) (uuid.UUID, error) {
	id, err := uuid.Parse(sellerID)
	if err != nil {
		return uuid.Nil, ErrSellerNotFound
	}
	return id, nil
}

func mapPurchaseError(err error) error {
	switch {
	case errors.Is(err, repository.ErrPurchaseNotFound):
		return ErrPurchaseNotFound
	case errors.Is(err, repository.ErrPurchaseState):
		return ErrPurchaseState
	case errors.Is(err, repository.ErrPurchaseKeyReused), errors.Is(err, repository.ErrProviderReferenceUsed):
		return fmt.Errorf("%w: %s", ErrInvalidPurchase, err.Error())
	case errors.Is(err, repository.ErrPurchaseAmount):
		return fmt.Errorf("%w: the amount paid does not buy a whole point", ErrInvalidPurchase)
	}
	return err
}

// Wallet is the seller's balance, reserved points, rate and history.
func (s *SellerPointsService) Wallet(ctx context.Context, sellerID string) (*models.SellerPointsWallet, error) {
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	w, err := s.points.SellerWallet(ctx, id, 100)
	if err != nil {
		return nil, err
	}
	w.Rate = s.rate
	w.PaymentProvider = s.provider.Name()
	w.TestPayments = s.provider.SelfConfirm()
	return w, nil
}

// Purchases is the seller's purchase history, newest first.
func (s *SellerPointsService) Purchases(ctx context.Context, sellerID string) ([]models.PointsPurchase, error) {
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	return s.points.ListSellerPurchases(ctx, id, 100)
}

// PurchaseInput is a seller asking to buy points: either the amount to pay
// or the number of points (or both, when they agree). The idempotency key
// makes a double-clicked Buy one purchase.
type PurchaseInput struct {
	AmountCents    int64  `json:"amount_cents"`
	Points         int64  `json:"points"`
	IdempotencyKey string `json:"idempotency_key"`
}

// quote works out a purchase's amount and points from what the seller
// asked for. The points always come from the amount at the configured rate;
// nothing the client sends about points is trusted beyond that.
func (s *SellerPointsService) quote(in PurchaseInput) (amount, points int64, err error) {
	rate := int64(s.rate.CentsPerPoint)
	amount = in.AmountCents
	if amount == 0 && in.Points > 0 {
		if in.Points > maxPurchaseCents/rate {
			return 0, 0, fmt.Errorf("%w: at most %d points in one purchase", ErrInvalidPurchase, maxPurchaseCents/rate)
		}
		amount = in.Points * rate
	}
	switch {
	case amount < minPurchaseCents || amount > maxPurchaseCents:
		return 0, 0, fmt.Errorf("%w: amount_cents must be %d to %d", ErrInvalidPurchase, minPurchaseCents, maxPurchaseCents)
	case amount%rate != 0:
		return 0, 0, fmt.Errorf("%w: amount must be a whole number of points (a multiple of %d cents)", ErrInvalidPurchase, rate)
	}
	points = amount / rate
	if in.Points > 0 && in.Points != points {
		return 0, 0, fmt.Errorf("%w: %d cents buys %d points, not %d", ErrInvalidPurchase, amount, points, in.Points)
	}
	return amount, points, nil
}

// CreatePurchase starts a purchase: pending until the payment is confirmed.
func (s *SellerPointsService) CreatePurchase(ctx context.Context, sellerID string, in PurchaseInput) (*models.PointsPurchase, error) {
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" || len(key) > 128 {
		return nil, fmt.Errorf("%w: idempotency_key of 1 to 128 characters is required", ErrInvalidPurchase)
	}
	amount, points, err := s.quote(in)
	if err != nil {
		return nil, err
	}
	p, err := s.points.CreatePurchase(ctx, &models.PointsPurchase{
		SellerID: id, AmountCents: amount, Currency: s.rate.Currency,
		CentsPerPoint: s.rate.CentsPerPoint, Points: points, Provider: s.provider.Name(),
	}, key)
	if err != nil {
		return nil, mapPurchaseError(err)
	}
	if p.Status == models.PointsPurchasePending && completesInstantly(s.provider) {
		ref := "instant-" + p.ID.String()
		p, _, err = s.points.CompletePurchase(ctx, p.ID, repository.PurchaseConfirmation{
			PaidAmountCents: p.AmountCents, ProviderReference: &ref, ConfirmedBy: "test",
		})
		return p, mapPurchaseError(err)
	}
	if p.Status == models.PointsPurchasePending && p.CheckoutURL == nil {
		url, reference, err := s.provider.StartCheckout(ctx, p)
		if err != nil {
			return nil, err
		}
		if url != nil || reference != nil {
			if err := s.points.SetPurchaseCheckout(ctx, p.ID, url, reference); err != nil {
				return nil, err
			}
			return s.points.GetPurchase(ctx, p.ID)
		}
	}
	return p, nil
}

// CancelPurchase abandons one of the seller's pending purchases.
func (s *SellerPointsService) CancelPurchase(ctx context.Context, sellerID string, purchaseID uuid.UUID) (*models.PointsPurchase, error) {
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	p, err := s.points.CancelPurchase(ctx, id, purchaseID)
	return p, mapPurchaseError(err)
}

// TestPayment completes (or declines) a seller's own purchase, as if the
// payment provider had confirmed it. Only the test provider allows this.
func (s *SellerPointsService) TestPayment(ctx context.Context, sellerID string, purchaseID uuid.UUID, succeed bool) (*models.PointsPurchase, error) {
	if !s.provider.SelfConfirm() {
		return nil, ErrTestPaymentsOff
	}
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	p, err := s.points.GetSellerPurchase(ctx, id, purchaseID)
	if err != nil {
		return nil, mapPurchaseError(err)
	}
	if !succeed {
		p, err = s.points.FailPurchase(ctx, p.ID, "Test payment declined", "test", nil, nil)
		return p, mapPurchaseError(err)
	}
	ref := "test-" + p.ID.String()
	p, _, err = s.points.CompletePurchase(ctx, p.ID, repository.PurchaseConfirmation{
		PaidAmountCents: p.AmountCents, ProviderReference: &ref, ConfirmedBy: "test",
	})
	return p, mapPurchaseError(err)
}

// PaymentEvent is a payment provider's confirmation, posted to the webhook.
type PaymentEvent struct {
	PurchaseID        uuid.UUID `json:"purchase_id"`
	Status            string    `json:"status"` // succeeded | failed
	AmountCents       int64     `json:"amount_cents"`
	Currency          string    `json:"currency"`
	ProviderReference *string   `json:"provider_reference"`
	FailureReason     string    `json:"failure_reason"`
}

// HandleWebhook applies a signed provider confirmation. The signature is a
// hex HMAC-SHA256 of the raw body with POINTS_WEBHOOK_SECRET. Points are
// credited from the amount the provider says was paid, never the amount
// asked for, and a confirmation sent twice is credited once.
func (s *SellerPointsService) HandleWebhook(ctx context.Context, body []byte, signature string) (*models.PointsPurchase, error) {
	if len(s.webhookSecret) == 0 {
		return nil, ErrWebhookNotConfigured
	}
	mac := hmac.New(sha256.New, s.webhookSecret)
	mac.Write(body)
	want := mac.Sum(nil)
	got, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(signature, "sha256=")))
	if err != nil || !hmac.Equal(want, got) {
		return nil, ErrWebhookSignature
	}
	var ev PaymentEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("%w: body is not a payment event", ErrInvalidPurchase)
	}
	p, err := s.points.GetPurchase(ctx, ev.PurchaseID)
	if err != nil {
		return nil, mapPurchaseError(err)
	}
	switch strings.ToLower(ev.Status) {
	case "succeeded", "completed", "paid":
		if !strings.EqualFold(ev.Currency, p.Currency) {
			return nil, fmt.Errorf("%w: paid in %s, the purchase is in %s", ErrInvalidPurchase, ev.Currency, p.Currency)
		}
		if ev.AmountCents <= 0 {
			return nil, fmt.Errorf("%w: amount_cents must be positive", ErrInvalidPurchase)
		}
		p, _, err = s.points.CompletePurchase(ctx, p.ID, repository.PurchaseConfirmation{
			PaidAmountCents: ev.AmountCents, ProviderReference: ev.ProviderReference, ConfirmedBy: "provider",
		})
	case "failed", "cancelled", "canceled", "declined":
		reason := strings.TrimSpace(ev.FailureReason)
		if reason == "" {
			reason = "Payment " + strings.ToLower(ev.Status)
		}
		p, err = s.points.FailPurchase(ctx, p.ID, reason, "provider", nil, nil)
	default:
		return nil, fmt.Errorf("%w: status must be succeeded or failed", ErrInvalidPurchase)
	}
	return p, mapPurchaseError(err)
}

// ─── Admin ────────────────────────────────────────────────────────────────

// AdminPurchases lists purchases for the admin, optionally one status.
func (s *SellerPointsService) AdminPurchases(ctx context.Context, status string) ([]models.PointsPurchase, error) {
	status = strings.TrimSpace(status)
	switch status {
	case "", models.PointsPurchasePending, models.PointsPurchaseCompleted,
		models.PointsPurchaseFailed, models.PointsPurchaseCancelled:
	default:
		return nil, fmt.Errorf("%w: status must be pending, completed, failed or cancelled", ErrInvalidPurchase)
	}
	return s.points.ListPurchases(ctx, status, 200)
}

// ConfirmPurchaseInput is an admin confirming a payment they have seen
// arrive. PaidAmountCents defaults to the amount asked for.
type ConfirmPurchaseInput struct {
	PaidAmountCents   *int64  `json:"paid_amount_cents"`
	ProviderReference *string `json:"provider_reference"`
	Note              string  `json:"note"`
}

// AdminConfirmPurchase credits a purchase whose payment an admin has
// confirmed, audited.
func (s *SellerPointsService) AdminConfirmPurchase(ctx context.Context, admin AdminActor, id uuid.UUID, in ConfirmPurchaseInput) (*models.PointsPurchase, error) {
	p, err := s.points.GetPurchase(ctx, id)
	if err != nil {
		return nil, mapPurchaseError(err)
	}
	paid := p.AmountCents
	if in.PaidAmountCents != nil {
		paid = *in.PaidAmountCents
	}
	if paid <= 0 || paid > maxPurchaseCents {
		return nil, fmt.Errorf("%w: paid_amount_cents must be 1 to %d", ErrInvalidPurchase, maxPurchaseCents)
	}
	note := strings.TrimSpace(in.Note)
	var reason *string
	if note != "" {
		reason = &note
	}
	audit := admin.audit("points.purchase_confirmed", id, reason)
	audit.EntityType = "points_purchase"
	adminID := admin.ID
	p, _, err = s.points.CompletePurchase(ctx, id, repository.PurchaseConfirmation{
		PaidAmountCents: paid, ProviderReference: in.ProviderReference, ConfirmedBy: "admin",
		AdminID: &adminID, Audit: &audit,
	})
	return p, mapPurchaseError(err)
}

// AdminFailPurchase marks a purchase failed, audited, with a reason.
func (s *SellerPointsService) AdminFailPurchase(ctx context.Context, admin AdminActor, id uuid.UUID, reason string) (*models.PointsPurchase, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, fmt.Errorf("%w: a reason is required", ErrInvalidPurchase)
	}
	audit := admin.audit("points.purchase_failed", id, &reason)
	audit.EntityType = "points_purchase"
	adminID := admin.ID
	p, err := s.points.FailPurchase(ctx, id, reason, "admin", &adminID, &audit)
	return p, mapPurchaseError(err)
}
