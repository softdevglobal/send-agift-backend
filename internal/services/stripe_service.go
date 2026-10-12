package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/repository"
)

var (
	ErrOrderAlreadyPaid = errors.New("this order is already paid")
	ErrOrderNotPayable  = errors.New("this order is not waiting for payment")
)

// StripeService takes customers' card payments on the platform account and
// pays delivered order lines out to sellers' Connect accounts.
//
// Money flow ("separate charges and transfers"):
//
//  1. The customer is sent to Stripe's hosted Checkout page for the order.
//  2. checkout.session.completed marks the order paid, and only then do the
//     confirmation email, the recipient's account and reward points happen.
//  3. When a seller's line is delivered, the payout loop transfers its value
//     (less the platform fee) to the seller's connected account.
//  4. Cancelling a paid order refunds the payment.
type StripeService struct {
	repo         *repository.StripeRepository
	client       *StripeClient // nil: Stripe is off
	platformHook string
	connectHook  string
	feeBPS       int
	webURL       string
	now          func() time.Time

	// connectCountry, when set, is the country new Connect accounts are
	// created in instead of the seller's own.
	connectCountry string

	// onPaid runs once when an order's payment is confirmed.
	onPaid func(ctx context.Context, orderID uuid.UUID)
	// onPointsCheckout applies a session that pays for a seller's points.
	onPointsCheckout func(ctx context.Context, cs StripeCheckoutSession) error
	// cancelUnpaid cancels an order nobody paid for.
	cancelUnpaid func(ctx context.Context, customerID, orderID string) error
}

func NewStripeService(repo *repository.StripeRepository, client *StripeClient, platformHookSecret, connectHookSecret string, feeBPS int, webURL string) *StripeService {
	return &StripeService{
		repo: repo, client: client,
		platformHook: platformHookSecret, connectHook: connectHookSecret,
		feeBPS: feeBPS, webURL: strings.TrimRight(webURL, "/"),
		now: time.Now,
	}
}

// UseConnectCountry creates sellers' Stripe accounts in this country (ISO
// code) instead of their own. For testing, or while a seller's country is
// one Stripe does not support.
func (s *StripeService) UseConnectCountry(iso string) {
	s.connectCountry = strings.ToUpper(strings.TrimSpace(iso))
}

// OnOrderPaid sets what happens once, when an order's payment is confirmed:
// its confirmation email, the recipient's account, reward points.
func (s *StripeService) OnOrderPaid(fn func(ctx context.Context, orderID uuid.UUID)) { s.onPaid = fn }

// OnPointsCheckout sets how a Checkout Session paying for a seller's points
// is applied (see SellerPointsService.HandleStripeCheckout).
func (s *StripeService) OnPointsCheckout(fn func(ctx context.Context, cs StripeCheckoutSession) error) {
	s.onPointsCheckout = fn
}

// CancelUnpaidWith sets how an abandoned, unpaid order is cancelled.
func (s *StripeService) CancelUnpaidWith(fn func(ctx context.Context, customerID, orderID string) error) {
	s.cancelUnpaid = fn
}

// Enabled is whether a secret key is set.
func (s *StripeService) Enabled() bool { return s.client != nil }

// OrderPayment is where to send the customer to pay for an order.
type OrderPayment struct {
	// URL is Stripe's hosted payment page.
	URL       string `json:"url"`
	SessionID string `json:"session_id"`
	Amount    int    `json:"amount"`
	Currency  string `json:"currency"`
}

// PayOrder starts (or, on a retry, returns the same) hosted payment for one
// of the customer's orders.
func (s *StripeService) PayOrder(ctx context.Context, customerID, orderID string) (*OrderPayment, error) {
	if s.client == nil {
		return nil, ErrStripeNotConfigured
	}
	customerUUID, err := uuid.Parse(customerID)
	if err != nil {
		return nil, repository.ErrStripeOrderNotFound
	}
	order, err := s.repo.GetPayableOrder(ctx, customerID, orderID)
	if err != nil {
		return nil, err
	}
	if order.Paid {
		return nil, ErrOrderAlreadyPaid
	}
	if order.Status != "pending_payment" || order.Total <= 0 {
		return nil, ErrOrderNotPayable
	}
	base := s.webURL + "/orders/" + order.ID.String()
	cs, err := s.client.CreateCheckoutSession(ctx, CheckoutParams{
		Lines:  s.receiptLines(ctx, order),
		Amount: order.Total, Currency: order.Currency,
		OrderID: order.ID.String(), OrderNumber: order.OrderNumber,
		CustomerEmail: order.CustomerEmail,
		SuccessURL:    base + "?placed=1&paid=1",
		CancelURL:     base + "?payment=cancelled",
	}, fmt.Sprintf("order-pay-v2-%s-%d", order.ID, order.Total))
	if err != nil {
		return nil, err
	}
	if cs.URL == "" {
		return nil, fmt.Errorf("stripe returned no checkout url for order %s", order.ID)
	}
	if err := s.repo.SavePayment(ctx, order.ID, customerUUID, cs.ID, order.Total, order.Currency); err != nil {
		return nil, err
	}
	return &OrderPayment{
		URL: cs.URL, SessionID: cs.ID,
		Amount: order.Total, Currency: strings.ToLower(order.Currency),
	}, nil
}

// receiptLines is what Stripe's payment page lists: each gift and each
// shop's delivery. They are only used when they add up to the order total;
// otherwise the page shows one line for the whole order.
func (s *StripeService) receiptLines(ctx context.Context, order *repository.PayableOrder) []CheckoutLine {
	rows, err := s.repo.CheckoutLines(ctx, order.ID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	sum := 0
	lines := make([]CheckoutLine, 0, len(rows))
	for _, r := range rows {
		if r.Quantity <= 0 || r.UnitAmount <= 0 {
			continue
		}
		name := r.Name
		if len(name) > 120 {
			name = name[:120]
		}
		lines = append(lines, CheckoutLine{Name: name, UnitAmount: r.UnitAmount, Quantity: r.Quantity})
		sum += r.UnitAmount * r.Quantity
	}
	if sum != order.Total {
		return nil
	}
	return lines
}

// SyncOrderPayment checks Stripe for an order's payment when the customer
// comes back, so the order does not depend on the webhook having arrived. It
// is the same settlement the webhook does, and safe to repeat. It returns the
// order's status afterwards.
func (s *StripeService) SyncOrderPayment(ctx context.Context, customerID, orderID string) (string, error) {
	order, err := s.repo.GetPayableOrder(ctx, customerID, orderID)
	if err != nil {
		return "", err
	}
	if s.client != nil && order.Status == "pending_payment" {
		pending, err := s.repo.PendingPayments(ctx, &order.ID, 5)
		if err != nil {
			return "", err
		}
		for _, id := range pending {
			if err := s.reconcileOne(ctx, id); err != nil {
				log.Printf("stripe: sync %s: %v", id, err)
			}
		}
		if order, err = s.repo.GetPayableOrder(ctx, customerID, orderID); err != nil {
			return "", err
		}
	}
	return order.Status, nil
}

// reconcileOne reads one payment from Stripe and applies what it says.
func (s *StripeService) reconcileOne(ctx context.Context, providerID string) error {
	switch {
	case strings.HasPrefix(providerID, "cs_"):
		cs, err := s.client.GetCheckoutSession(ctx, providerID)
		if err != nil {
			return err
		}
		switch {
		case cs.PaymentStatus == "paid":
			return s.settle(ctx, *cs)
		case cs.Status == "expired":
			return s.repo.MarkCheckoutExpired(ctx, cs.ID)
		}
	case strings.HasPrefix(providerID, "pi_"):
		pi, err := s.client.GetPaymentIntent(ctx, providerID)
		if err != nil {
			return err
		}
		if pi.Status == "succeeded" {
			return s.settle(ctx, StripeCheckoutSession{ID: pi.ID, PaymentIntent: pi.ID, PaymentStatus: "paid"})
		}
	}
	return nil
}

// ReconcilePending confirms recent payments straight from Stripe. The
// webhook is the fast path; this makes an order still turn paid when a
// webhook was missed (a listener that was not running, an outage).
func (s *StripeService) ReconcilePending(ctx context.Context, limit int) (int, error) {
	if s.client == nil {
		return 0, nil
	}
	pending, err := s.repo.PendingPayments(ctx, nil, limit)
	if err != nil {
		return 0, err
	}
	for _, id := range pending {
		if err := s.reconcileOne(ctx, id); err != nil {
			log.Printf("stripe: reconcile %s: %v", id, err)
		}
	}
	return len(pending), nil
}

// RefundOrder returns a paid order's money to the customer, for an order
// that was cancelled. Nothing happens for an order that was never paid.
func (s *StripeService) RefundOrder(ctx context.Context, orderID uuid.UUID) error {
	if s.client == nil {
		return nil
	}
	intent, err := s.repo.PaidIntentForOrder(ctx, orderID)
	if err != nil || intent == "" {
		return err
	}
	return s.client.CreateRefund(ctx, intent, "refund-order-"+orderID.String())
}

// HandlePlatformWebhook applies an event from the "your account" endpoint.
func (s *StripeService) HandlePlatformWebhook(ctx context.Context, body []byte, signature string) error {
	ev, err := VerifyStripeWebhook(body, signature, s.platformHook, s.now())
	if err != nil {
		return err
	}
	return s.handleOnce(ctx, ev, func() error {
		switch ev.Type {
		case "checkout.session.completed", "checkout.session.async_payment_succeeded":
			var cs StripeCheckoutSession
			if err := json.Unmarshal(ev.Data.Object, &cs); err != nil {
				return err
			}
			if cs.Metadata["kind"] == pointsCheckoutKind {
				if s.onPointsCheckout == nil {
					return nil
				}
				return s.onPointsCheckout(ctx, cs)
			}
			if cs.PaymentStatus != "paid" {
				return nil // card still pending (a bank step); a later event settles it
			}
			return s.settle(ctx, cs)
		case "checkout.session.expired":
			var cs StripeCheckoutSession
			if err := json.Unmarshal(ev.Data.Object, &cs); err != nil {
				return err
			}
			if cs.Metadata["kind"] == pointsCheckoutKind {
				if s.onPointsCheckout == nil {
					return nil
				}
				return s.onPointsCheckout(ctx, cs)
			}
			return s.repo.MarkCheckoutExpired(ctx, cs.ID)
		case "charge.refunded":
			var ch struct {
				PaymentIntent  string `json:"payment_intent"`
				AmountRefunded int    `json:"amount_refunded"`
			}
			if err := json.Unmarshal(ev.Data.Object, &ch); err != nil {
				return err
			}
			if ch.PaymentIntent != "" {
				return s.repo.ApplyRefund(ctx, ch.PaymentIntent, ch.AmountRefunded)
			}
		}
		return nil
	})
}

// settle applies a confirmed payment: the order becomes paid and, once, what
// follows a payment runs. A payment for an order cancelled meanwhile is
// refunded instead.
func (s *StripeService) settle(ctx context.Context, cs StripeCheckoutSession) error {
	paid, err := s.repo.MarkCheckoutPaid(ctx, cs.ID, cs.PaymentIntent)
	if err != nil || paid == nil {
		return err
	}
	if paid.OrderStatus == "cancelled" || paid.OrderStatus == "refunded" {
		log.Printf("stripe: order %s was cancelled before payment %s arrived; refunding", paid.OrderID, cs.PaymentIntent)
		if paid.PaymentIntentID == "" || s.client == nil {
			return nil
		}
		return s.client.CreateRefund(ctx, paid.PaymentIntentID, "refund-order-"+paid.OrderID.String())
	}
	log.Printf("stripe: order %s paid (%s)", paid.OrderID, cs.PaymentIntent)
	if paid.First && s.onPaid != nil {
		s.onPaid(ctx, paid.OrderID)
	}
	return nil
}

// HandleConnectWebhook applies an event from the "connected accounts"
// endpoint. Events for accounts that are not a SendAGift seller change
// nothing, since this Stripe account may serve other projects too.
func (s *StripeService) HandleConnectWebhook(ctx context.Context, body []byte, signature string) error {
	ev, err := VerifyStripeWebhook(body, signature, s.connectHook, s.now())
	if err != nil {
		return err
	}
	return s.handleOnce(ctx, ev, func() error {
		switch ev.Type {
		case "account.updated":
			var a StripeAccount
			if err := json.Unmarshal(ev.Data.Object, &a); err != nil {
				return err
			}
			id := a.ID
			if id == "" {
				id = ev.Account
			}
			return s.repo.UpdateAccountStatus(ctx, id, a.accountStatus())
		case "capability.updated":
			if s.client == nil || ev.Account == "" {
				return nil
			}
			a, err := s.client.GetAccount(ctx, ev.Account)
			if err != nil {
				return err
			}
			return s.repo.UpdateAccountStatus(ctx, ev.Account, a.accountStatus())
		case "account.application.deauthorized":
			return s.repo.UpdateAccountStatus(ctx, ev.Account, repository.AccountStatus{Disabled: true})
		}
		return nil
	})
}

// handleOnce runs fn the first time an event id is seen. If fn fails the
// event is forgotten, so Stripe's retry runs it again.
func (s *StripeService) handleOnce(ctx context.Context, ev *StripeEvent, fn func() error) error {
	first, err := s.repo.RecordEvent(ctx, ev.ID)
	if err != nil {
		return err
	}
	if !first {
		return nil
	}
	if err := fn(); err != nil {
		s.repo.ForgetEvent(ctx, ev.ID)
		return err
	}
	return nil
}

// SellerPayouts is a seller's Stripe status as the console shows it.
type SellerPayouts struct {
	Enabled          bool   `json:"enabled"` // Stripe is configured on this server
	Connected        bool   `json:"connected"`
	ChargesEnabled   bool   `json:"charges_enabled"`
	PayoutsEnabled   bool   `json:"payouts_enabled"`
	DetailsSubmitted bool   `json:"details_submitted"`
	AccountID        string `json:"account_id,omitempty"`
}

// SellerStatus reads the seller's account, refreshing it from Stripe when
// there is one.
func (s *StripeService) SellerStatus(ctx context.Context, sellerID string) (*SellerPayouts, error) {
	st, err := s.repo.GetSellerStripe(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	if s.client != nil && st.AccountID != "" {
		if a, err := s.client.GetAccount(ctx, st.AccountID); err == nil {
			_ = s.repo.UpdateAccountStatus(ctx, st.AccountID, a.accountStatus())
			st.ChargesEnabled, st.PayoutsEnabled, st.DetailsSubmitted = a.ChargesEnabled, a.PayoutsEnabled, a.DetailsSubmitted
		}
	}
	return &SellerPayouts{
		Enabled: s.client != nil, Connected: st.AccountID != "",
		ChargesEnabled: st.ChargesEnabled, PayoutsEnabled: st.PayoutsEnabled,
		DetailsSubmitted: st.DetailsSubmitted, AccountID: st.AccountID,
	}, nil
}

// PayoutHistory is a seller's payout page: their account, what they have
// been paid and what is still to come, and the latest settlements.
type PayoutHistory struct {
	Account SellerPayouts             `json:"account"`
	Totals  []repository.PayoutTotals `json:"totals"`
	Items   []repository.PayoutRow    `json:"items"`
}

// SellerPayoutHistory reads the seller's payout page.
func (s *StripeService) SellerPayoutHistory(ctx context.Context, sellerID string) (*PayoutHistory, error) {
	account, err := s.SellerStatus(ctx, sellerID)
	if err != nil {
		return nil, err
	}
	items, totals, err := s.repo.SellerPayouts(ctx, sellerID, 100)
	if err != nil {
		return nil, err
	}
	return &PayoutHistory{Account: *account, Totals: totals, Items: items}, nil
}

// ConnectOnboarding returns a Stripe-hosted page where the seller adds their
// bank and identity details, creating their connected account first if
// needed.
func (s *StripeService) ConnectOnboarding(ctx context.Context, sellerID string) (string, error) {
	if s.client == nil {
		return "", ErrStripeNotConfigured
	}
	st, err := s.repo.GetSellerStripe(ctx, sellerID)
	if err != nil {
		return "", err
	}
	accountID := st.AccountID
	if accountID == "" {
		country := st.CountryISO
		if s.connectCountry != "" {
			country = s.connectCountry
		}
		a, err := s.client.CreateAccount(ctx, st.Email, country, sellerID)
		if err != nil {
			return "", err
		}
		accountID = a.ID
		if err := s.repo.SetSellerAccount(ctx, sellerID, accountID); err != nil {
			return "", err
		}
	}
	return s.client.CreateAccountLink(ctx, accountID,
		s.webURL+"/seller/payouts?stripe=refresh",
		s.webURL+"/seller/payouts?stripe=return")
}

// platformFee is the platform's cut of one line, in minor units.
func platformFee(amount, bps int) int {
	if bps <= 0 || amount <= 0 {
		return 0
	}
	return amount * bps / 10000
}

// PayDueTransfers pays out delivered lines. It returns how many it sent.
// A line that cannot be sent now (say the platform balance is short) is
// logged and retried on the next round.
func (s *StripeService) PayDueTransfers(ctx context.Context, limit int) (int, error) {
	if s.client == nil {
		return 0, nil
	}
	due, err := s.repo.DueTransfers(ctx, limit)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, d := range due {
		fee := platformFee(d.Amount, s.feeBPS)
		net := d.Amount - fee
		if net <= 0 {
			continue
		}
		tr, err := s.client.CreateTransfer(ctx, net, d.Currency, d.AccountID,
			"order_"+d.OrderNumber,
			map[string]string{
				"order_item_id": d.OrderItemID.String(),
				"order_id":      d.OrderID.String(),
				"seller_id":     d.SellerID.String(),
			},
			"transfer-"+d.OrderItemID.String())
		if err != nil {
			log.Printf("stripe transfer for item %s: %v", d.OrderItemID, err)
			continue
		}
		if err := s.repo.RecordTransfer(ctx, d, tr.ID, fee); err != nil {
			log.Printf("stripe transfer %s sent but not recorded: %v", tr.ID, err)
			continue
		}
		sent++
	}
	return sent, nil
}

// RunPayoutLoop pays out delivered lines on an interval until ctx ends. With
// several API servers only one takes each round (see Exclusive).
func (s *StripeService) RunPayoutLoop(ctx context.Context, every time.Duration, exclusive Exclusive) {
	if s.client == nil {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	round := func() {
		if _, err := exclusive.run(ctx, func(ctx context.Context) error {
			// Confirm payments first, so a just-paid, just-delivered line
			// is settled in the same round.
			if _, err := s.ReconcilePending(ctx, 50); err != nil {
				log.Printf("stripe reconcile: %v", err)
			}
			n, err := s.PayDueTransfers(ctx, 50)
			if n > 0 {
				log.Printf("stripe: paid out %d order line(s) to sellers", n)
			}
			return err
		}); err != nil {
			log.Printf("stripe payouts: %v", err)
		}
	}
	round()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			round()
		}
	}
}

// unpaidOrderHours is how long an order may wait for payment. Stripe's own
// hosted page lapses after 24 hours, so by now it can no longer be paid.
const unpaidOrderHours = 25

// CancelExpiredUnpaid cancels orders nobody paid for, which also gives back
// the points held on them. It returns how many it cancelled.
func (s *StripeService) CancelExpiredUnpaid(ctx context.Context, limit int) (int, error) {
	if s.cancelUnpaid == nil {
		return 0, nil
	}
	orders, err := s.repo.ExpiredUnpaidOrders(ctx, unpaidOrderHours, limit)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range orders {
		if err := s.cancelUnpaid(ctx, o.CustomerID.String(), o.OrderID.String()); err != nil {
			log.Printf("stripe: could not cancel unpaid order %s: %v", o.OrderID, err)
			continue
		}
		n++
	}
	return n, nil
}

// RunExpiryLoop cancels abandoned unpaid orders on an interval until ctx
// ends. With several API servers only one takes each round.
func (s *StripeService) RunExpiryLoop(ctx context.Context, every time.Duration, exclusive Exclusive) {
	if s.client == nil {
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
				n, err := s.CancelExpiredUnpaid(ctx, 100)
				if n > 0 {
					log.Printf("stripe: cancelled %d unpaid order(s)", n)
				}
				return err
			}); err != nil {
				log.Printf("stripe expiry: %v", err)
			}
		}
	}
}
