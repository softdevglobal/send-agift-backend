package services

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"myapp/internal/repository"
)

func sampleOrderSummary() *repository.OrderEmailSummary {
	str := func(s string) *string { return &s }
	img := "https://images.unsplash.com/photo-1549465220-1a8b9238cd48?w=200"
	return &repository.OrderEmailSummary{
		OrderID:        uuid.MustParse("6f1d2c1e-8a8e-4c55-9c39-0f5f7b1f2a10"),
		OrderNumber:    "SAG-20261005-1A2B3C4D",
		CountryID:      uuid.New(),
		DeliveryDate:   time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
		SubtotalAmount: 7450,
		DeliveryAmount: 0,
		TotalAmount:    7450,
		Currency:       "AUD",
		GiftMessage:    str("Happy birthday! Thinking of you today <3"),
		GiftPoints:     250,
		CustomerEmail:  "sam@example.com",
		CustomerName:   str("Sam Perera"),
		RecipientName:  str("Alex Silva"),
		RecipientEmail: str("alex@example.com"),
		RecipientCity:  str("Melbourne"),
		Items: []repository.OrderEmailItem{
			{ProductName: "Sunrise Bouquet", ImageURL: &img, ShopName: "Bloom & Co", Quantity: 1, TotalAmount: 5450},
			{ProductName: "Salted Caramel Truffles", ShopName: "Cocoa Lane", Quantity: 2, TotalAmount: 2000},
		},
	}
}

// Every email renders, escapes what customers typed, and says what it must.
func TestEmailTemplatesRender(t *testing.T) {
	const web = "https://sendagift.test"
	order := sampleOrderSummary()

	cases := map[string]struct {
		render func() (*EmailContent, error)
		want   []string
	}{
		"customer_welcome": {func() (*EmailContent, error) { return renderCustomerWelcome(web, "Sam") }, []string{"Hi Sam,", "A little something", web + "/products", "cid:" + emailLogoCID}},
		"seller_code": {func() (*EmailContent, error) { return renderSellerEmailCode(web, "Kim", "042917", 15*time.Minute) },
			[]string{"042917 is your SendAGift verification code", ">0<", ">7<", "15 minutes"}},
		"seller_pending":  {func() (*EmailContent, error) { return renderSellerPendingReview(web, "Kim") }, []string{"Pending review", "DONE"}},
		"seller_approved": {func() (*EmailContent, error) { return renderSellerApproved(web, "Kim", "Welcome aboard!") }, []string{"account is now active", "Welcome aboard!", web + "/seller/shops"}},
		"seller_rejected": {func() (*EmailContent, error) { return renderSellerRejected(web, "Kim", "Please add your ABN.") }, []string{"Please add your ABN.", web + "/seller/profile"}},
		"order_placed": {func() (*EmailContent, error) { return renderOrderPlaced(web, order) },
			[]string{"Your gift for Alex Silva", "AUD 54.50", "AUD 74.50", "Free", "Surprise intact", "&lt;3"}},
		"gift_delivered": {func() (*EmailContent, error) { return renderGiftDelivered(web, order, GiftRecipientDefaultPassword) },
			[]string{"Sam sent you", "00001111", "alex@example.com", "250 points", "next=%2Faccount%2Fgifts"}},
	}

	dir := os.Getenv("EMAIL_PREVIEW_DIR")
	for name, tc := range cases {
		c, err := tc.render()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.Subject == "" || c.Text == "" {
			t.Fatalf("%s: empty subject or text", name)
		}
		for _, w := range tc.want {
			if !strings.Contains(c.HTML, w) && !strings.Contains(c.Subject, w) {
				t.Errorf("%s: missing %q", name, w)
			}
		}
		if strings.Contains(c.HTML, "<3\"") || strings.Contains(c.HTML, "ZgotmplZ") {
			t.Errorf("%s: unescaped or rejected value in HTML", name)
		}
		if dir != "" {
			// Previews have no attachment to resolve cid: against.
			logo := "data:image/png;base64," + base64.StdEncoding.EncodeToString(emailLogoPNG)
			html := strings.ReplaceAll(c.HTML, "cid:"+emailLogoCID, logo)
			_ = os.WriteFile(filepath.Join(dir, name+".html"), []byte(html), 0o644)
		}
	}

	// Prices stay out of the recipient's email.
	c, _ := renderGiftDelivered(web, order, "")
	if strings.Contains(c.HTML, "AUD") || strings.Contains(c.HTML, "00001111") {
		t.Error("gift_delivered: shows prices, or a password the recipient already changed")
	}
}

func TestFormatMoney(t *testing.T) {
	for _, tc := range []struct {
		minor    int
		currency string
		want     string
	}{
		{7450, "aud", "AUD 74.50"},
		{123456789, "USD", "USD 1,234,567.89"},
		{5, "LKR", "LKR 0.05"},
		{1500, "JPY", "JPY 1,500"},
	} {
		if got := formatMoney(tc.minor, tc.currency); got != tc.want {
			t.Errorf("formatMoney(%d, %s) = %q, want %q", tc.minor, tc.currency, got, tc.want)
		}
	}
}

func TestEmailCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := newEmailCode()
		if err != nil || len(c) != 6 || strings.Trim(c, "0123456789") != "" {
			t.Fatalf("bad code %q: %v", c, err)
		}
		seen[c] = true
	}
	if len(seen) < 190 {
		t.Fatalf("codes repeat too often: %d distinct of 200", len(seen))
	}
	a, b := uuid.New(), uuid.New()
	if hashEmailCode(a, "123456") == hashEmailCode(b, "123456") {
		t.Fatal("code hash must depend on the seller")
	}
}

func TestLogoIsAttachedOnlyWhenShown(t *testing.T) {
	if got := inlineImagesFor(`<img src="cid:` + emailLogoCID + `">`); len(got) != 1 || got[0].CID != emailLogoCID || got[0].Content == "" {
		t.Fatalf("logo not attached: %+v", got)
	}
	if got := inlineImagesFor("<p>no logo</p>"); got != nil {
		t.Fatal("attached a logo the email doesn't show")
	}
}
