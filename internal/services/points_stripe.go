package services

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// pointsCheckoutKind marks a Stripe Checkout Session that pays for a
// seller's points rather than a customer's order.
const pointsCheckoutKind = "points_purchase"

// stripePointsPayments takes the payment for a points purchase on Stripe's
// hosted Checkout page. The purchase is credited when Stripe confirms it:
// by webhook, by the seller coming back, or by the reconcile loop.
type stripePointsPayments struct {
	client *StripeClient
	webURL string
}

// NewStripePointsProvider is the Stripe points payment provider.
func NewStripePointsProvider(client *StripeClient, webURL string) (PointsPaymentProvider, error) {
	if client == nil {
		return nil, fmt.Errorf("POINTS_PAYMENT_PROVIDER=stripe needs STRIPE_SECRET_KEY")
	}
	return &stripePointsPayments{client: client, webURL: strings.TrimRight(webURL, "/")}, nil
}

func (p *stripePointsPayments) Name() string      { return "stripe" }
func (p *stripePointsPayments) SelfConfirm() bool { return false }

// StartCheckout opens a payment page for the purchase and returns its URL
// and the session id, which becomes the purchase's provider reference.
func (p *stripePointsPayments) StartCheckout(ctx context.Context, pp *models.PointsPurchase) (*string, *string, error) {
	name := fmt.Sprintf("%d SendAGift points", pp.Points)
	if pp.PackageName != nil {
		name = fmt.Sprintf("%s package: %d points", *pp.PackageName, pp.Points)
		if pp.BonusPoints > 0 {
			name = fmt.Sprintf("%s package: %d points (incl. %d bonus)", *pp.PackageName, pp.Points, pp.BonusPoints)
		}
	}
	back := p.webURL + "/seller/points?purchase=" + pp.ID.String()
	cs, err := p.client.CreateCheckoutSession(ctx, CheckoutParams{
		Lines:       []CheckoutLine{{Name: name, UnitAmount: int(pp.AmountCents), Quantity: 1}},
		Amount:      int(pp.AmountCents),
		Currency:    pp.Currency,
		OrderNumber: "points",
		SuccessURL:  back + "&paid=1",
		CancelURL:   back + "&cancelled=1",
		Metadata: map[string]string{
			"kind":        pointsCheckoutKind,
			"purchase_id": pp.ID.String(),
			"seller_id":   pp.SellerID.String(),
		},
	}, "points-pay-"+pp.ID.String())
	if err != nil {
		return nil, nil, err
	}
	if cs.URL == "" {
		return nil, nil, fmt.Errorf("stripe returned no checkout url for points purchase %s", pp.ID)
	}
	return &cs.URL, &cs.ID, nil
}

// HandleStripeCheckout applies a Stripe Checkout Session that pays for a
// points purchase: credit it when paid, fail it when the page lapsed. It is
// the same whether it comes from the webhook or a re-check, and safe to
// repeat: a purchase is only ever credited once.
func (s *SellerPointsService) HandleStripeCheckout(ctx context.Context, cs StripeCheckoutSession) error {
	id, err := uuid.Parse(cs.Metadata["purchase_id"])
	if err != nil {
		return nil // not one of ours
	}
	p, err := s.points.GetPurchase(ctx, id)
	if err != nil {
		return mapPurchaseError(err)
	}
	if p.Status != models.PointsPurchasePending {
		return nil
	}
	switch {
	case cs.PaymentStatus == "paid":
		if !strings.EqualFold(cs.Currency, p.Currency) {
			return fmt.Errorf("%w: paid in %s, the purchase is in %s", ErrInvalidPurchase, cs.Currency, p.Currency)
		}
		paid := cs.AmountTotal
		if paid <= 0 {
			paid = p.AmountCents
		}
		_, credited, err := s.points.CompletePurchase(ctx, id, repository.PurchaseConfirmation{
			PaidAmountCents: paid, ConfirmedBy: "provider",
		})
		if err != nil {
			return mapPurchaseError(err)
		}
		if credited {
			log.Printf("points: purchase %s paid on Stripe (%d %s)", id, paid, strings.ToUpper(cs.Currency))
		}
	case cs.Status == "expired":
		_, err := s.points.FailPurchase(ctx, id, "The payment page expired before it was paid", "provider", nil, nil)
		if err != nil && mapPurchaseError(err) != ErrPurchaseState {
			return mapPurchaseError(err)
		}
	}
	return nil
}

// stripeClient is the Stripe client behind the configured provider, if it
// is Stripe.
func (s *SellerPointsService) stripeClient() *StripeClient {
	if sp, ok := s.provider.(*stripePointsPayments); ok {
		return sp.client
	}
	return nil
}

// SyncPurchase re-checks one of the seller's purchases with Stripe, for
// when they come back from paying and the webhook has not arrived yet.
func (s *SellerPointsService) SyncPurchase(ctx context.Context, sellerID string, purchaseID uuid.UUID) (*models.PointsPurchase, error) {
	id, err := parseSeller(sellerID)
	if err != nil {
		return nil, err
	}
	p, err := s.points.GetSellerPurchase(ctx, id, purchaseID)
	if err != nil {
		return nil, mapPurchaseError(err)
	}
	if err := s.recheck(ctx, p); err != nil {
		log.Printf("points: re-check purchase %s: %v", p.ID, err)
	}
	p, err = s.points.GetSellerPurchase(ctx, id, purchaseID)
	return p, mapPurchaseError(err)
}

func (s *SellerPointsService) recheck(ctx context.Context, p *models.PointsPurchase) error {
	client := s.stripeClient()
	if client == nil || p.Status != models.PointsPurchasePending || p.ProviderReference == nil ||
		!strings.HasPrefix(*p.ProviderReference, "cs_") {
		return nil
	}
	cs, err := client.GetCheckoutSession(ctx, *p.ProviderReference)
	if err != nil {
		return err
	}
	if cs.Metadata == nil {
		cs.Metadata = map[string]string{}
	}
	cs.Metadata["purchase_id"] = p.ID.String()
	return s.HandleStripeCheckout(ctx, *cs)
}

// ReconcileStripePurchases re-checks recent pending Stripe purchases, so a
// paid purchase is credited even when its webhook never arrives.
func (s *SellerPointsService) ReconcileStripePurchases(ctx context.Context, limit int) (int, error) {
	if s.stripeClient() == nil {
		return 0, nil
	}
	pending, err := s.points.PendingPurchasesFor(ctx, "stripe", limit)
	if err != nil {
		return 0, err
	}
	for i := range pending {
		if err := s.recheck(ctx, &pending[i]); err != nil {
			log.Printf("points: reconcile purchase %s: %v", pending[i].ID, err)
		}
	}
	return len(pending), nil
}

// RunReconcileLoop re-checks pending Stripe purchases on an interval until
// ctx ends. With several API servers only one takes each round.
func (s *SellerPointsService) RunReconcileLoop(ctx context.Context, every time.Duration, exclusive Exclusive) {
	if s.stripeClient() == nil {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := exclusive.run(ctx, func(ctx context.Context) error {
				_, err := s.ReconcileStripePurchases(ctx, 50)
				return err
			}); err != nil {
				log.Printf("points reconcile: %v", err)
			}
		}
	}
}
