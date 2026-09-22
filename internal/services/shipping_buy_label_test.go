package services

import (
	"encoding/json"
	"testing"
)

func TestRateMatchesCheckout_SameUSPSFirstClass(t *testing.T) {
	selected := &CheckoutSelectedRate{
		Provider:    "USPS",
		ServiceName: "First Class Package International Service",
		Amount:      4633,
		Currency:    "USD",
	}
	if !rateMatchesCheckout("USPS", "First Class Package International Service", selected) {
		t.Fatal("expected exact USPS First Class match")
	}
}

func TestRateMatchesCheckout_RejectsDifferentCourier(t *testing.T) {
	selected := &CheckoutSelectedRate{
		Provider:    "USPS",
		ServiceName: "First Class Package International Service",
		Amount:      4633,
		Currency:    "USD",
	}
	if rateMatchesCheckout("DHL Express", "Worldwide", selected) {
		t.Fatal("DHL must not match USPS First Class")
	}
}

func TestRecommendedRateIDFromMetadata_UsesStoredRecommended(t *testing.T) {
	selected := &CheckoutSelectedRate{
		Provider:    "USPS",
		ServiceName: "First Class Package International Service",
	}
	meta, _ := json.Marshal(map[string]any{
		"recommended_rate_object_id": "rate_fresh_usps",
		"checkout_quote": map[string]any{
			"provider":     "USPS",
			"service_name": "First Class Package International Service",
			"amount":       4633,
			"currency":     "USD",
			"source":       "checkout_quote",
		},
		"rates": []map[string]any{
			{"object_id": "rate_dhl", "provider": "DHL Express", "servicelevel": map[string]string{"name": "Worldwide"}},
			{"object_id": "rate_fresh_usps", "provider": "USPS", "servicelevel": map[string]string{"name": "First Class Package International Service"}},
		},
	})
	got := recommendedRateIDFromMetadata(meta, selected)
	if got != "rate_fresh_usps" {
		t.Fatalf("got %q want rate_fresh_usps", got)
	}
}

func TestRecommendedRateIDFromMetadata_MatchesFromRatesList(t *testing.T) {
	selected := &CheckoutSelectedRate{
		Provider:    "USPS",
		ServiceName: "First Class Package International Service",
	}
	meta, _ := json.Marshal(map[string]any{
		"rates": []map[string]any{
			{"object_id": "rate_dhl", "provider": "DHL Express", "servicelevel": map[string]string{"name": "Worldwide"}},
			{"object_id": "rate_usps", "provider": "USPS", "servicelevel": map[string]string{"name": "First Class Package International Service"}},
		},
	})
	got := recommendedRateIDFromMetadata(meta, selected)
	if got != "rate_usps" {
		t.Fatalf("got %q want rate_usps", got)
	}
}

func TestFormatMinorMoney(t *testing.T) {
	if got := formatMinorMoney(4633, "USD"); got != "46.33 USD" {
		t.Fatalf("got %q", got)
	}
	if got := formatMinorMoney(7951, "USD"); got != "79.51 USD" {
		t.Fatalf("got %q", got)
	}
}
