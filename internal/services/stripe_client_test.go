package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signStripe(secret string, at time.Time, body string) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyStripeWebhook(t *testing.T) {
	now := time.Now()
	body := `{"id":"evt_1","type":"payment_intent.succeeded","data":{"object":{"id":"pi_1"}}}`
	good := signStripe("whsec_a", now, body)

	ev, err := VerifyStripeWebhook([]byte(body), good, "whsec_a", now)
	if err != nil || ev.ID != "evt_1" || ev.Type != "payment_intent.succeeded" {
		t.Fatalf("valid signature: ev=%+v err=%v", ev, err)
	}

	cases := map[string]struct {
		header, secret string
		at             time.Time
		want           error
	}{
		"wrong secret":   {good, "whsec_b", now, ErrStripeSignature},
		"tampered body":  {signStripe("whsec_a", now, "{}"), "whsec_a", now, ErrStripeSignature},
		"too old":        {signStripe("whsec_a", now.Add(-time.Hour), body), "whsec_a", now, ErrStripeSignature},
		"missing header": {"", "whsec_a", now, ErrStripeSignature},
		"no secret set":  {good, "", now, ErrStripeNotConfigured},
	}
	for name, c := range cases {
		if _, err := VerifyStripeWebhook([]byte(body), c.header, c.secret, c.at); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", name, err, c.want)
		}
	}
}

func TestPlatformFee(t *testing.T) {
	for _, c := range []struct{ amount, bps, want int }{
		{10_000, 0, 0},
		{10_000, 1000, 1000}, // 10%
		{999, 250, 24},       // rounds down
		{0, 500, 0},
	} {
		if got := platformFee(c.amount, c.bps); got != c.want {
			t.Errorf("platformFee(%d, %d) = %d, want %d", c.amount, c.bps, got, c.want)
		}
	}
}

func TestStripeClientTransferSendsIdempotencyKey(t *testing.T) {
	var gotKey, gotAuth, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		gotPath = r.URL.Path
		user, _, _ := r.BasicAuth()
		gotAuth = user
		_ = r.ParseForm()
		gotBody = r.PostForm.Encode()
		_, _ = w.Write([]byte(`{"id":"tr_123"}`))
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_x")
	c.base = srv.URL
	tr, err := c.CreateTransfer(context.Background(), 900, "AUD", "acct_1", "order_X",
		map[string]string{"order_item_id": "it-1"}, "transfer-it-1")
	if err != nil || tr.ID != "tr_123" {
		t.Fatalf("transfer: %+v %v", tr, err)
	}
	if gotPath != "/transfers" || gotKey != "transfer-it-1" || gotAuth != "sk_test_x" {
		t.Errorf("path=%q key=%q auth=%q", gotPath, gotKey, gotAuth)
	}
	for _, want := range []string{"amount=900", "currency=aud", "destination=acct_1", "transfer_group=order_X"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body %q missing %q", gotBody, want)
		}
	}
}

func TestStripeClientTransferCanDrawOnTheCharge(t *testing.T) {
	var gotBody, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		_ = r.ParseForm()
		gotBody = r.PostForm.Encode()
		_, _ = w.Write([]byte(`{"id":"tr_9"}`))
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_x")
	c.base = srv.URL
	if _, err := c.CreateTransferFrom(context.Background(), 500, "aud", "acct_1", "order_X",
		"ch_42", nil, transferKey("it-1", "ch_42")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, "source_transaction=ch_42") {
		t.Errorf("body %q missing source_transaction", gotBody)
	}
	if gotKey != "transfer-src-it-1" {
		t.Errorf("key = %q; want a key apart from the plain transfer's", gotKey)
	}
	// Without a charge the body and key are as before.
	if _, err := c.CreateTransfer(context.Background(), 500, "aud", "acct_1", "", nil, transferKey("it-1", "")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "source_transaction") || gotKey != "transfer-it-1" {
		t.Errorf("plain transfer body=%q key=%q", gotBody, gotKey)
	}
}

func TestStripeClientMapsAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"balance_insufficient","message":"no funds"}}`))
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_x")
	c.base = srv.URL
	_, err := c.CreateTransfer(context.Background(), 1, "aud", "acct_1", "", nil, "k")
	var apiErr *StripeAPIError
	if !errors.As(err, &apiErr) || apiErr.Code != "balance_insufficient" || apiErr.Status != 400 {
		t.Fatalf("got %v", err)
	}
}

func TestNewStripeClientIsNilWithoutKey(t *testing.T) {
	if NewStripeClient("  ") != nil {
		t.Fatal("expected nil client when no key is set")
	}
}

func TestStripeClientCheckoutSession(t *testing.T) {
	var gotPath, gotKey, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("Idempotency-Key")
		_ = r.ParseForm()
		gotBody = r.PostForm.Encode()
		_, _ = w.Write([]byte(`{"id":"cs_test_1","url":"https://checkout.stripe.com/c/pay/cs_test_1","payment_status":"unpaid"}`))
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_x")
	c.base = srv.URL
	cs, err := c.CreateCheckoutSession(context.Background(), CheckoutParams{
		Amount: 2500, Currency: "AUD", OrderID: "ord-1", OrderNumber: "SG-100",
		SuccessURL: "http://app/orders/ord-1?paid=1", CancelURL: "http://app/orders/ord-1",
	}, "order-pay-ord-1-2500")
	if err != nil || cs.ID != "cs_test_1" || cs.URL == "" {
		t.Fatalf("session: %+v %v", cs, err)
	}
	if gotPath != "/checkout/sessions" || gotKey != "order-pay-ord-1-2500" {
		t.Errorf("path=%q key=%q", gotPath, gotKey)
	}
	for _, want := range []string{
		"mode=payment", "line_items%5B0%5D%5Bprice_data%5D%5Bunit_amount%5D=2500",
		"line_items%5B0%5D%5Bprice_data%5D%5Bcurrency%5D=aud", "client_reference_id=ord-1",
		"payment_intent_data%5Btransfer_group%5D=order_SG-100",
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body %q missing %q", gotBody, want)
		}
	}
}

func TestStripeClientRefundIsKeyedPerOrder(t *testing.T) {
	var gotPath, gotKey, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("Idempotency-Key")
		_ = r.ParseForm()
		gotBody = r.PostForm.Encode()
		_, _ = w.Write([]byte(`{"id":"re_1"}`))
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_x")
	c.base = srv.URL
	if err := c.CreateRefund(context.Background(), "pi_123", "refund-order-ord-1"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/refunds" || gotKey != "refund-order-ord-1" || gotBody != "payment_intent=pi_123" {
		t.Errorf("path=%q key=%q body=%q", gotPath, gotKey, gotBody)
	}
}

func TestStripeClientCheckoutShowsEachLine(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotBody = r.PostForm.Encode()
		_, _ = w.Write([]byte(`{"id":"cs_1","url":"https://x"}`))
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_x")
	c.base = srv.URL
	_, err := c.CreateCheckoutSession(context.Background(), CheckoutParams{
		Amount: 3000, Currency: "USD", OrderID: "o", OrderNumber: "SG-1",
		Lines: []CheckoutLine{
			{Name: "Chocolate box", UnitAmount: 1250, Quantity: 2},
			{Name: "Delivery · Sweet Shop", UnitAmount: 500, Quantity: 1},
		},
	}, "k")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"line_items%5B0%5D%5Bprice_data%5D%5Bproduct_data%5D%5Bname%5D=Chocolate+box",
		"line_items%5B0%5D%5Bprice_data%5D%5Bunit_amount%5D=1250",
		"line_items%5B0%5D%5Bquantity%5D=2",
		"line_items%5B1%5D%5Bprice_data%5D%5Bproduct_data%5D%5Bname%5D=Delivery",
		"line_items%5B1%5D%5Bprice_data%5D%5Bunit_amount%5D=500",
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body %q missing %q", gotBody, want)
		}
	}
}

func TestStripeAccountKeyFollowsTheRequest(t *testing.T) {
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		_, _ = w.Write([]byte(`{"id":"acct_1"}`))
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_x")
	c.base = srv.URL
	for _, country := range []string{"LK", "AU", "AU"} {
		if _, err := c.CreateAccount(context.Background(), "s@example.test", country, "seller-1"); err != nil {
			t.Fatal(err)
		}
	}
	if keys[0] == keys[1] {
		t.Errorf("a changed country reused the key %q, which Stripe rejects", keys[0])
	}
	if keys[1] != keys[2] {
		t.Errorf("the same request got keys %q and %q; a retry must reuse one", keys[1], keys[2])
	}
	if !strings.HasPrefix(keys[0], "seller-account-seller-1-") {
		t.Errorf("key = %q", keys[0])
	}
}

func TestStripeClientPaymentIntentForTheApp(t *testing.T) {
	var gotBody, gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("Idempotency-Key")
		_ = r.ParseForm()
		gotBody = r.PostForm.Encode()
		_, _ = w.Write([]byte(`{"id":"pi_1","status":"requires_payment_method","client_secret":"pi_1_secret_x"}`))
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_x")
	c.base = srv.URL
	pi, err := c.CreatePaymentIntent(context.Background(), PaymentIntentParams{
		Amount: 38500, Currency: "AUD", OrderID: "o-1", OrderNumber: "SAG-1", CustomerEmail: "a@b.co",
	}, "order-pi-v1-o-1-38500")
	if err != nil || pi.ClientSecret != "pi_1_secret_x" {
		t.Fatalf("intent = %+v, %v", pi, err)
	}
	if gotPath != "/payment_intents" || gotKey != "order-pi-v1-o-1-38500" {
		t.Errorf("path=%q key=%q", gotPath, gotKey)
	}
	for _, want := range []string{"amount=38500", "currency=aud", "metadata%5Border_id%5D=o-1",
		"transfer_group=order_SAG-1", "receipt_email=a%40b.co"} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("body %q missing %q", gotBody, want)
		}
	}
}
