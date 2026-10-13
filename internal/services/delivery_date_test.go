package services

import (
	"errors"
	"strings"
	"testing"
	"time"

	"myapp/internal/repository"
)

// shopOption is a shop that can deliver the day after now, judged at the
// shop's local clock, with no cutoff.
func shopOption(now time.Time, days int) *SellerDeliveryOption {
	return &SellerDeliveryOption{
		Mode:                  SellerDeliveryModeName,
		Available:             true,
		EstimatedDays:         days,
		EstimatedDeliveryDate: now.AddDate(0, 0, days).Format("2006-01-02"),
		localNow:              now,
		shopName:              "Bay Area Gifts",
	}
}

func TestCheckDeliveryDate(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	const shop, product = "shop-1", "product-1"
	line := []dateLine{{ProductID: product, ShopID: shop, Quantity: 1}}
	options := map[string]*SellerDeliveryOption{shop: shopOption(now, 1)}
	stocked := map[string]repository.ProductDateRules{
		product: {Name: "Headset", HasInventory: true, AvailableQty: 5},
	}

	t.Run("a date the shop can meet", func(t *testing.T) {
		if got := checkDeliveryDate("2026-10-14", line, options, stocked); len(got) != 0 {
			t.Fatalf("want no problems, got %v", got)
		}
	})

	t.Run("a day in the past", func(t *testing.T) {
		got := checkDeliveryDate("2026-10-10", line, options, stocked)
		if len(got) != 1 || !strings.Contains(got[0], "today or a later date") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("sooner than the zone allows", func(t *testing.T) {
		got := checkDeliveryDate("2026-10-12", line, options, stocked)
		if len(got) != 1 || !strings.Contains(got[0], "earliest is 2026-10-13") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("sooner than preparation allows", func(t *testing.T) {
		rules := map[string]repository.ProductDateRules{
			product: {Name: "Headset", PrepMinutes: 3 * 24 * 60},
		}
		opt := map[string]*SellerDeliveryOption{shop: shopOption(now, 0)}
		opt[shop].CutoffTime = "18:00"
		got := checkDeliveryDate("2026-10-13", line, opt, rules)
		if len(got) != 1 || !strings.Contains(got[0], "more preparation time") {
			t.Fatalf("got %v", got)
		}
		if got := checkDeliveryDate("2026-10-16", line, opt, rules); len(got) != 0 {
			t.Fatalf("a date after the preparation time should work, got %v", got)
		}
	})

	t.Run("a day the seller blocked", func(t *testing.T) {
		rules := map[string]repository.ProductDateRules{
			product: {
				Name: "Headset", HasInventory: true, AvailableQty: 5,
				UnavailableDates: []time.Time{time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)},
			},
		}
		got := checkDeliveryDate("2026-10-15", line, options, rules)
		if len(got) != 1 || !strings.Contains(got[0], "cannot be delivered on 2026-10-15") {
			t.Fatalf("got %v", got)
		}
		if got := checkDeliveryDate("2026-10-16", line, options, rules); len(got) != 0 {
			t.Fatalf("the next day should work, got %v", got)
		}
	})

	t.Run("out of stock, and not enough stock", func(t *testing.T) {
		out := map[string]repository.ProductDateRules{
			product: {Name: "Headset", HasInventory: true, AvailableQty: 2, ReservedQty: 2},
		}
		if got := checkDeliveryDate("2026-10-14", line, options, out); len(got) != 1 || !strings.Contains(got[0], "out of stock") {
			t.Fatalf("got %v", got)
		}
		low := map[string]repository.ProductDateRules{
			product: {Name: "Headset", HasInventory: true, AvailableQty: 3, ReservedQty: 1},
		}
		two := []dateLine{{ProductID: product, ShopID: shop, Quantity: 3}}
		if got := checkDeliveryDate("2026-10-14", two, options, low); len(got) != 1 || !strings.Contains(got[0], "only 2 of Headset left") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("the same product on two lines counts together", func(t *testing.T) {
		rules := map[string]repository.ProductDateRules{
			product: {Name: "Headset", HasInventory: true, AvailableQty: 3},
		}
		lines := []dateLine{
			{ProductID: product, ShopID: shop, Quantity: 2},
			{ProductID: product, ShopID: shop, Quantity: 2},
		}
		if got := checkDeliveryDate("2026-10-14", lines, options, rules); len(got) != 1 || !strings.Contains(got[0], "only 3 of Headset left") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("a gift whose stock was never set is allowed", func(t *testing.T) {
		rules := map[string]repository.ProductDateRules{product: {Name: "Headset"}}
		if got := checkDeliveryDate("2026-10-14", line, options, rules); len(got) != 0 {
			t.Fatalf("want no problems, got %v", got)
		}
	})

	t.Run("a shop that cannot deliver at all is left to pricing", func(t *testing.T) {
		opt := map[string]*SellerDeliveryOption{shop: {Mode: SellerDeliveryModeName, Available: false}}
		if got := checkDeliveryDate("2026-10-10", line, opt, stocked); len(got) != 0 {
			t.Fatalf("want no problems, got %v", got)
		}
	})
}

func TestDeliveryDateErrorWrapsTheFirstReason(t *testing.T) {
	if deliveryDateError(nil) != nil {
		t.Fatal("no problems must be no error")
	}
	err := deliveryDateError([]string{"Headset is out of stock", "another"})
	if !errors.Is(err, ErrDeliveryDate) || !strings.Contains(err.Error(), "Headset is out of stock") {
		t.Fatalf("got %v", err)
	}
}
