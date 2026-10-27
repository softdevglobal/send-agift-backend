package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Stripe Billing for memberships. Checkout runs in subscription mode with
// the price given inline, so an offer version needs no Stripe Price set up
// by hand; a new version simply sells at its own price. Renewals and
// cancellations arrive as webhooks (see HandleMembershipEvent).

// membershipCheckoutKind marks a Checkout Session and its subscription as a
// membership, in their metadata.
const membershipCheckoutKind = "membership"

// SubscriptionCheckoutParams is one membership offer to be paid on Stripe's
// hosted page.
type SubscriptionCheckoutParams struct {
	SubscriptionID string // ours, carried in metadata
	OfferName      string
	OfferID        string
	Currency       string
	PriceMinor     int64
	IntervalUnit   string // day, week, month or year
	IntervalCount  int
	CustomerEmail  string
	SuccessURL     string
	CancelURL      string
}

// CreateSubscriptionCheckout starts a hosted page that sets up a renewing
// membership. The redirect back does not activate anything: only a paid
// invoice, by webhook, creates a paid period.
func (c *StripeClient) CreateSubscriptionCheckout(ctx context.Context, p SubscriptionCheckoutParams, idempotencyKey string) (*StripeCheckoutSession, error) {
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(p.Currency))
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(p.PriceMinor, 10))
	form.Set("line_items[0][price_data][recurring][interval]", p.IntervalUnit)
	form.Set("line_items[0][price_data][recurring][interval_count]", strconv.Itoa(p.IntervalCount))
	form.Set("line_items[0][price_data][product_data][name]", p.OfferName)
	form.Set("success_url", p.SuccessURL)
	form.Set("cancel_url", p.CancelURL)
	form.Set("client_reference_id", p.SubscriptionID)
	for _, prefix := range []string{"metadata", "subscription_data[metadata]"} {
		form.Set(prefix+"[kind]", membershipCheckoutKind)
		form.Set(prefix+"[membership_subscription_id]", p.SubscriptionID)
		form.Set(prefix+"[membership_offer_id]", p.OfferID)
	}
	if p.CustomerEmail != "" {
		form.Set("customer_email", p.CustomerEmail)
	}
	var cs StripeCheckoutSession
	if err := c.do(ctx, http.MethodPost, "/checkout/sessions", form, idempotencyKey, &cs); err != nil {
		return nil, err
	}
	return &cs, nil
}

// StripeSubscription is the part of a Stripe subscription the app reads.
type StripeSubscription struct {
	ID                string            `json:"id"`
	Customer          string            `json:"customer"`
	Status            string            `json:"status"`
	CancelAtPeriodEnd bool              `json:"cancel_at_period_end"`
	CurrentPeriodEnd  int64             `json:"current_period_end"`
	EndedAt           int64             `json:"ended_at"`
	Metadata          map[string]string `json:"metadata"`
	// Newer API versions keep the period on each item.
	Items struct {
		Data []struct {
			CurrentPeriodEnd int64 `json:"current_period_end"`
		} `json:"data"`
	} `json:"items"`
}

// PeriodEnd is the end of the current period, wherever this API version
// puts it.
func (s *StripeSubscription) PeriodEnd() *time.Time {
	end := s.CurrentPeriodEnd
	if end == 0 && len(s.Items.Data) > 0 {
		end = s.Items.Data[0].CurrentPeriodEnd
	}
	return unixTime(end)
}

func unixTime(sec int64) *time.Time {
	if sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

// CancelSubscriptionAtPeriodEnd stops a membership renewing. Benefits run
// to the end of the period already paid for.
func (c *StripeClient) CancelSubscriptionAtPeriodEnd(ctx context.Context, id, idempotencyKey string) (*StripeSubscription, error) {
	form := url.Values{}
	form.Set("cancel_at_period_end", "true")
	var sub StripeSubscription
	if err := c.do(ctx, http.MethodPost, "/subscriptions/"+url.PathEscape(id), form, idempotencyKey, &sub); err != nil {
		return nil, err
	}
	return &sub, nil
}

// GetSubscription reads a subscription's current state.
func (c *StripeClient) GetSubscription(ctx context.Context, id string) (*StripeSubscription, error) {
	var sub StripeSubscription
	if err := c.do(ctx, http.MethodGet, "/subscriptions/"+url.PathEscape(id), nil, "", &sub); err != nil {
		return nil, err
	}
	return &sub, nil
}

// PaidInvoices lists a subscription's most recent paid invoices.
func (c *StripeClient) PaidInvoices(ctx context.Context, subscriptionID string) ([]StripeInvoice, error) {
	var out struct {
		Data []StripeInvoice `json:"data"`
	}
	q := url.Values{}
	q.Set("subscription", subscriptionID)
	q.Set("status", "paid")
	q.Set("limit", "5")
	if err := c.do(ctx, http.MethodGet, "/invoices", q, "", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// StripeInvoice is the part of a paid invoice that becomes a paid period.
// Field locations differ between API versions, so both are read.
type StripeInvoice struct {
	ID           string `json:"id"`
	Subscription string `json:"subscription"`
	Status       string `json:"status"`
	AmountPaid   int64  `json:"amount_paid"`
	Currency     string `json:"currency"`
	// Older versions: the payment intent and charge on the invoice.
	PaymentIntent string `json:"payment_intent"`
	Charge        string `json:"charge"`
	PeriodStart   int64  `json:"period_start"`
	PeriodEnd     int64  `json:"period_end"`
	// Newer versions: the subscription under parent.
	Parent struct {
		SubscriptionDetails struct {
			Subscription string            `json:"subscription"`
			Metadata     map[string]string `json:"metadata"`
		} `json:"subscription_details"`
	} `json:"parent"`
	SubscriptionDetails struct {
		Metadata map[string]string `json:"metadata"`
	} `json:"subscription_details"`
	StatusTransitions struct {
		PaidAt int64 `json:"paid_at"`
	} `json:"status_transitions"`
	Lines struct {
		Data []struct {
			Period struct {
				Start int64 `json:"start"`
				End   int64 `json:"end"`
			} `json:"period"`
		} `json:"data"`
	} `json:"lines"`
}

// SubscriptionID is the subscription the invoice bills.
func (i *StripeInvoice) SubscriptionID() string {
	if i.Subscription != "" {
		return i.Subscription
	}
	return i.Parent.SubscriptionDetails.Subscription
}

// Metadata is the subscription's metadata as copied onto the invoice.
func (i *StripeInvoice) Metadata() map[string]string {
	if len(i.Parent.SubscriptionDetails.Metadata) > 0 {
		return i.Parent.SubscriptionDetails.Metadata
	}
	return i.SubscriptionDetails.Metadata
}

// Period is the service period the invoice paid for: its subscription
// line's period, which for a renewal is the coming period (the invoice's own
// period_start/period_end describe the previous one).
func (i *StripeInvoice) Period() (start, end time.Time, ok bool) {
	for _, l := range i.Lines.Data {
		if l.Period.End > l.Period.Start && l.Period.Start > 0 {
			return time.Unix(l.Period.Start, 0).UTC(), time.Unix(l.Period.End, 0).UTC(), true
		}
	}
	return time.Time{}, time.Time{}, false
}

// decodeInvoice reads a webhook's invoice object.
func decodeInvoice(raw json.RawMessage) (*StripeInvoice, error) {
	var inv StripeInvoice
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, err
	}
	return &inv, nil
}
