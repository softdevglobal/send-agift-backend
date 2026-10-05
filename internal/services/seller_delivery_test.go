package services

import (
	"strings"
	"testing"
	"time"

	"myapp/internal/models"
)

func f64(v float64) *float64 { return &v }

func TestBuildSellerDeliveryOption(t *testing.T) {
	zones := []models.ShopDeliveryZone{
		{MaxKm: 3, PriceAmount: 0, Currency: "USD", EstimatedDays: 0},
		{MaxKm: 5, PriceAmount: 5000, Currency: "USD", EstimatedDays: 1},
		{MaxKm: 10, PriceAmount: 8000, Currency: "USD", EstimatedDays: 2},
	}
	now := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	shopLat, shopLng := f64(6.9271), f64(79.8612) // Colombo

	// ~4.4 km north → 5 km band.
	opt := buildSellerDeliveryOption(shopLat, shopLng, f64(6.9671), f64(79.8612), zones, now)
	if !opt.Available || opt.MaxKm != 5 || opt.PriceAmount != 5000 || opt.EstimatedDays != 1 {
		t.Fatalf("want 5 km band, got %+v", opt)
	}
	if opt.EstimatedDeliveryDate != "2026-09-26" {
		t.Fatalf("want 2026-09-26, got %s", opt.EstimatedDeliveryDate)
	}

	// Same point → free, same day.
	opt = buildSellerDeliveryOption(shopLat, shopLng, shopLat, shopLng, zones, now)
	if !opt.Available || !opt.IsFree || opt.MaxKm != 3 || opt.EstimatedDays != 0 {
		t.Fatalf("want free 3 km band, got %+v", opt)
	}

	cutoff := "14:00"
	zones[0].CutoffTime = &cutoff
	before := time.Date(2026, 9, 25, 13, 30, 0, 0, time.UTC)
	opt = buildSellerDeliveryOption(shopLat, shopLng, shopLat, shopLng, zones, before)
	if opt.EstimatedDays != 0 || opt.EstimatedDeliveryDate != "2026-09-25" || opt.CutoffTime != "14:00" {
		t.Fatalf("want same day before cutoff, got %+v", opt)
	}
	after := time.Date(2026, 9, 25, 14, 1, 0, 0, time.UTC)
	opt = buildSellerDeliveryOption(shopLat, shopLng, shopLat, shopLng, zones, after)
	if opt.EstimatedDays != 1 || opt.EstimatedDeliveryDate != "2026-09-26" {
		t.Fatalf("want next day after cutoff, got %+v", opt)
	}

	// ~22 km → outside.
	opt = buildSellerDeliveryOption(shopLat, shopLng, f64(7.1271), f64(79.8612), zones, now)
	if opt.Available || !strings.Contains(opt.Reason, "farthest delivery zone is 10.00 km") || !opt.hasZoneAndPoints() {
		t.Fatalf("want outside zones, got %+v", opt)
	}

	// Missing recipient point → unavailable but not a distance failure.
	opt = buildSellerDeliveryOption(shopLat, shopLng, nil, nil, zones, now)
	if opt.Available || opt.hasZoneAndPoints() {
		t.Fatalf("want missing-coordinates result, got %+v", opt)
	}
}
