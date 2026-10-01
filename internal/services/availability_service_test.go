package services

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

func TestShopAvailabilityUsesZoneAndDate(t *testing.T) {
	shopID := uuid.New()
	near := uuid.New()
	soldOut := uuid.New()
	blocked := uuid.New()
	shop := repository.ShopForAvailability{
		ID:        shopID,
		Name:      "Colombo Gifts",
		Latitude:  f64(6.9271),
		Longitude: f64(79.8612),
		Zones: []models.ShopDeliveryZone{
			{MaxKm: 5, PriceAmount: 5000, Currency: "LKR", EstimatedDays: 1},
		},
	}
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	gifts := []repository.GiftStock{
		{ID: near, ShopID: shopID},
		{ID: soldOut, ShopID: shopID, HasInventory: true, AvailableQty: 1, ReservedQty: 1},
		{ID: blocked, ShopID: shopID, UnavailableDates: []time.Time{time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)}},
	}
	q := GiftAvailabilityQuery{Latitude: 6.95, Longitude: 79.8612, DeliveryDate: "2026-10-02"}

	got := shopAvailability(shop, gifts, q, now)
	if got == nil || len(got.ProductIDs) != 1 || got.ProductIDs[0] != near.String() {
		t.Fatalf("want only the in-stock gift, got %+v", got)
	}
	if got.MaxKm != 5 || got.PriceAmount != 5000 || got.IsFree {
		t.Fatalf("want the 5 km zone price, got %+v", got)
	}
	if got.EstimatedDeliveryDate != "2026-10-01" {
		t.Fatalf("want arrival 2026-10-01, got %s", got.EstimatedDeliveryDate)
	}

	q.DeliveryDate = "2026-09-30"
	if shopAvailability(shop, gifts, q, now) != nil {
		t.Fatal("a 1-day zone cannot arrive the same day")
	}

	far := shop
	far.Zones = []models.ShopDeliveryZone{{MaxKm: 1, PriceAmount: 0, Currency: "LKR", EstimatedDays: 0}}
	if shopAvailability(far, gifts, q, now) != nil {
		t.Fatal("a point outside the zone must not be available")
	}
}
