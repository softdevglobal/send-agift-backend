package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"myapp/internal/repository"
)

// A thin client for the parts of the Stripe REST API the marketplace uses.
// Stripe's API is plain form-encoded HTTPS, so this avoids pulling in the
// whole SDK for five calls.

const stripeAPIBase = "https://api.stripe.com/v1"

// webhookTolerance is how old a signed webhook may be before it is refused,
// which stops a captured request being replayed later.
const webhookTolerance = 5 * time.Minute

var (
	ErrStripeNotConfigured = errors.New("stripe is not configured")
	ErrStripeSignature     = errors.New("stripe webhook signature is not valid")
)

// StripeAPIError is a failure answered by Stripe.
type StripeAPIError struct {
	Status  int
	Type    string
	Code    string
	Message string
}

func (e *StripeAPIError) Error() string {
	return fmt.Sprintf("stripe: %s (%d %s %s)", e.Message, e.Status, e.Type, e.Code)
}

// StripeClient calls Stripe with the platform's secret key.
type StripeClient struct {
	key  string
	base string
	http *http.Client
}

// NewStripeClient returns nil when there is no key, so callers can tell
// "Stripe is off" from a failure.
func NewStripeClient(secretKey string) *StripeClient {
	if strings.TrimSpace(secretKey) == "" {
		return nil
	}
	return &StripeClient{
		key:  strings.TrimSpace(secretKey),
		base: stripeAPIBase,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

// do sends one form-encoded request. idempotencyKey makes a retried POST
// return the first result instead of acting twice.
func (c *StripeClient) do(ctx context.Context, method, path string, form url.Values, idempotencyKey string, out any) error {
	var body io.Reader
	endpoint := c.base + path
	if method == http.MethodGet {
		if len(form) > 0 {
			endpoint += "?" + form.Encode()
		}
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.key, "")
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var env struct {
			Error struct {
				Type    string `json:"type"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &env)
		return &StripeAPIError{
			Status: resp.StatusCode, Type: env.Error.Type,
			Code: env.Error.Code, Message: env.Error.Message,
		}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// StripeCheckoutSession is the part of a Checkout Session the app reads.
type StripeCheckoutSession struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Status        string `json:"status"` // open | complete | expired
	PaymentStatus string `json:"payment_status"`
	// PaymentIntent is the card payment behind a completed session.
	PaymentIntent string `json:"payment_intent"`
}

// CheckoutLine is one row on Stripe's payment page: a gift, or a delivery.
type CheckoutLine struct {
	Name       string
	UnitAmount int
	Quantity   int
}

// CheckoutParams describes one order to be paid on Stripe's hosted page.
type CheckoutParams struct {
	// Lines are shown as the receipt. They must add up to Amount; with none
	// the page shows one line for the whole order.
	Lines         []CheckoutLine
	Amount        int
	Currency      string
	OrderID       string
	OrderNumber   string
	CustomerEmail string
	SuccessURL    string
	CancelURL     string
}

// CreateCheckoutSession starts a hosted payment page for an order. The
// customer is sent to its URL, and Stripe's webhook (not the redirect back)
// is what tells us the money arrived. transfer_group ties the later
// transfers to sellers back to this payment.
func (c *StripeClient) CreateCheckoutSession(ctx context.Context, p CheckoutParams, idempotencyKey string) (*StripeCheckoutSession, error) {
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("payment_method_types[0]", "card")
	lines := p.Lines
	if len(lines) == 0 {
		lines = []CheckoutLine{{Name: "SendAGift order " + p.OrderNumber, UnitAmount: p.Amount, Quantity: 1}}
	}
	for i, l := range lines {
		k := "line_items[" + strconv.Itoa(i) + "]"
		form.Set(k+"[quantity]", strconv.Itoa(l.Quantity))
		form.Set(k+"[price_data][currency]", strings.ToLower(p.Currency))
		form.Set(k+"[price_data][unit_amount]", strconv.Itoa(l.UnitAmount))
		form.Set(k+"[price_data][product_data][name]", l.Name)
	}
	form.Set("success_url", p.SuccessURL)
	form.Set("cancel_url", p.CancelURL)
	form.Set("client_reference_id", p.OrderID)
	form.Set("metadata[order_id]", p.OrderID)
	form.Set("metadata[order_number]", p.OrderNumber)
	form.Set("payment_intent_data[transfer_group]", "order_"+p.OrderNumber)
	form.Set("payment_intent_data[metadata][order_id]", p.OrderID)
	if p.CustomerEmail != "" {
		form.Set("customer_email", p.CustomerEmail)
	}
	var cs StripeCheckoutSession
	if err := c.do(ctx, http.MethodPost, "/checkout/sessions", form, idempotencyKey, &cs); err != nil {
		return nil, err
	}
	return &cs, nil
}

// GetCheckoutSession reads a session's current state, which is how a payment
// is confirmed when its webhook did not arrive.
func (c *StripeClient) GetCheckoutSession(ctx context.Context, id string) (*StripeCheckoutSession, error) {
	var cs StripeCheckoutSession
	if err := c.do(ctx, http.MethodGet, "/checkout/sessions/"+url.PathEscape(id), nil, "", &cs); err != nil {
		return nil, err
	}
	return &cs, nil
}

// StripePaymentIntent is the part of a PaymentIntent read back.
type StripePaymentIntent struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// GetPaymentIntent reads a card payment's current state.
func (c *StripeClient) GetPaymentIntent(ctx context.Context, id string) (*StripePaymentIntent, error) {
	var pi StripePaymentIntent
	if err := c.do(ctx, http.MethodGet, "/payment_intents/"+url.PathEscape(id), nil, "", &pi); err != nil {
		return nil, err
	}
	return &pi, nil
}

// CreateRefund returns a whole payment to the customer. The key makes a
// retried request refund once.
func (c *StripeClient) CreateRefund(ctx context.Context, paymentIntentID, idempotencyKey string) error {
	form := url.Values{}
	form.Set("payment_intent", paymentIntentID)
	return c.do(ctx, http.MethodPost, "/refunds", form, idempotencyKey, nil)
}

// StripeAccount is a connected account's state.
type StripeAccount struct {
	ID               string          `json:"id"`
	ChargesEnabled   bool            `json:"charges_enabled"`
	PayoutsEnabled   bool            `json:"payouts_enabled"`
	DetailsSubmitted bool            `json:"details_submitted"`
	Requirements     json.RawMessage `json:"requirements"`
}

// accountStatus is the repository's view of the account.
func (a *StripeAccount) accountStatus() repository.AccountStatus {
	return repository.AccountStatus{
		ChargesEnabled: a.ChargesEnabled, PayoutsEnabled: a.PayoutsEnabled,
		DetailsSubmitted: a.DetailsSubmitted, Requirements: a.Requirements,
	}
}

// CreateAccount makes an Express connected account that can receive
// transfers from the platform.
func (c *StripeClient) CreateAccount(ctx context.Context, email, country, sellerID string) (*StripeAccount, error) {
	form := url.Values{}
	form.Set("type", "express")
	if email != "" {
		form.Set("email", email)
	}
	if country != "" {
		form.Set("country", strings.ToUpper(country))
	}
	form.Set("capabilities[transfers][requested]", "true")
	form.Set("metadata[seller_id]", sellerID)
	// The key follows the request's contents. Stripe refuses a key reused
	// with different parameters (say, after the country was changed), and
	// the same request retried must still map to one account.
	sum := sha256.Sum256([]byte(form.Encode()))
	key := "seller-account-" + sellerID + "-" + hex.EncodeToString(sum[:6])
	var acct StripeAccount
	if err := c.do(ctx, http.MethodPost, "/accounts", form, key, &acct); err != nil {
		return nil, err
	}
	return &acct, nil
}

// GetAccount reads a connected account's current state.
func (c *StripeClient) GetAccount(ctx context.Context, id string) (*StripeAccount, error) {
	var acct StripeAccount
	if err := c.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(id), nil, "", &acct); err != nil {
		return nil, err
	}
	return &acct, nil
}

// CreateAccountLink returns a one-time onboarding URL for the seller.
func (c *StripeClient) CreateAccountLink(ctx context.Context, accountID, refreshURL, returnURL string) (string, error) {
	form := url.Values{}
	form.Set("account", accountID)
	form.Set("refresh_url", refreshURL)
	form.Set("return_url", returnURL)
	form.Set("type", "account_onboarding")
	var link struct {
		URL string `json:"url"`
	}
	if err := c.do(ctx, http.MethodPost, "/account_links", form, "", &link); err != nil {
		return "", err
	}
	return link.URL, nil
}

// StripeTransfer is money moved from the platform to a connected account.
type StripeTransfer struct {
	ID string `json:"id"`
}

// CreateTransfer sends amount to a connected account. The idempotency key is
// the order line, so a retry can never pay it twice.
func (c *StripeClient) CreateTransfer(ctx context.Context, amount int, currency, destination, transferGroup string, metadata map[string]string, idempotencyKey string) (*StripeTransfer, error) {
	form := url.Values{}
	form.Set("amount", strconv.Itoa(amount))
	form.Set("currency", strings.ToLower(currency))
	form.Set("destination", destination)
	if transferGroup != "" {
		form.Set("transfer_group", transferGroup)
	}
	for k, v := range metadata {
		form.Set("metadata["+k+"]", v)
	}
	var tr StripeTransfer
	if err := c.do(ctx, http.MethodPost, "/transfers", form, idempotencyKey, &tr); err != nil {
		return nil, err
	}
	return &tr, nil
}

// StripeEvent is a webhook event, with its object kept raw until its type is
// known.
type StripeEvent struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Account string `json:"account"`
	Data    struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// VerifyStripeWebhook checks the Stripe-Signature header against the raw
// body with the endpoint's signing secret, then decodes the event. The
// header looks like "t=1700000000,v1=<hex>[,v1=<hex>]".
func VerifyStripeWebhook(payload []byte, header, secret string, now time.Time) (*StripeEvent, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, ErrStripeNotConfigured
	}
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == "" || len(sigs) == 0 {
		return nil, ErrStripeSignature
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil, ErrStripeSignature
	}
	if d := now.Sub(time.Unix(sec, 0)); d > webhookTolerance || d < -webhookTolerance {
		return nil, ErrStripeSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	want := mac.Sum(nil)
	valid := false
	for _, s := range sigs {
		got, err := hex.DecodeString(s)
		if err == nil && hmac.Equal(got, want) {
			valid = true
		}
	}
	if !valid {
		return nil, ErrStripeSignature
	}
	var ev StripeEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("stripe event: %w", err)
	}
	return &ev, nil
}
