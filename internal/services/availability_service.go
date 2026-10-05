package services

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/repository"
)

// GiftAvailabilityQuery is the Find gifts search: where it is going, and the
// day it should arrive. DeliveryDate is yyyy-mm-dd and may be empty.
type GiftAvailabilityQuery struct {
	Latitude     float64
	Longitude    float64
	DeliveryDate string
	CustomerType string
}

// ShopGiftAvailability is one shop that can deliver to the searched point,
// priced from the matching delivery zone, plus the gifts that can be sent.
type ShopGiftAvailability struct {
	ShopID                string             `json:"shop_id"`
	ShopName              string             `json:"shop_name"`
	DistanceKm            *float64           `json:"distance_km,omitempty"`
	MaxKm                 float64            `json:"max_km"`
	PriceAmount           int                `json:"price_amount"`
	Currency              string             `json:"currency,omitempty"`
	IsFree                bool               `json:"is_free"`
	EstimatedDays         int                `json:"estimated_days"`
	CutoffTime            string             `json:"cutoff_time,omitempty"`
	EstimatedDeliveryDate string             `json:"estimated_delivery_date,omitempty"`
	ProductIDs            []string           `json:"product_ids"`
	Products              []AvailableProduct `json:"products"`
}

// AvailableProduct is a published gift the gifts page can render without
// loading every shop's catalog.
type AvailableProduct struct {
	ID           string   `json:"id"`
	ShopID       string   `json:"shop_id"`
	Name         string   `json:"name"`
	Slug         string   `json:"slug"`
	Description  *string  `json:"description,omitempty"`
	PriceAmount  int      `json:"price_amount"`
	Currency     string   `json:"currency"`
	ImageURL     *string  `json:"image_url,omitempty"`
	OccasionTags []string `json:"occasion_tags"`
	Status       string   `json:"status"`
	// StockLeft is set when sellable quantity is at or below the low-stock threshold.
	StockLeft *int `json:"stock_left,omitempty"`
}

// GiftAvailability is the Find gifts result for one destination.
type GiftAvailability struct {
	Latitude     float64                `json:"latitude"`
	Longitude    float64                `json:"longitude"`
	DeliveryDate string                 `json:"delivery_date,omitempty"`
	Shops        []ShopGiftAvailability `json:"shops"`
}

// AvailabilityService decides which published gifts can reach a searched address.
type AvailabilityService struct {
	repo *repository.AvailabilityRepository
	now  func() time.Time
}

func NewAvailabilityService(repo *repository.AvailabilityRepository) *AvailabilityService {
	return &AvailabilityService{repo: repo, now: time.Now}
}

// Search returns shops whose delivery zones cover the destination and whose
// gifts are in stock for the requested arrival day.
func (s *AvailabilityService) Search(ctx context.Context, q GiftAvailabilityQuery) (*GiftAvailability, error) {
	customerType, err := normalizeCustomerType(q.CustomerType)
	if err != nil {
		return nil, err
	}
	// SQL already dropped shops whose farthest zone cannot reach this point.
	candidates, err := s.repo.ListShopsInReach(ctx, q.Latitude, q.Longitude)
	if err != nil {
		return nil, err
	}

	now := s.now()
	reachable := make([]repository.ShopForAvailability, 0, len(candidates))
	reachableIDs := make([]uuid.UUID, 0, len(candidates))
	for _, shop := range candidates {
		// Cutoff and preparation are wall-clock times in the shop's timezone.
		if !zoneReaches(shop, q, shopLocalNow(now, shop.Timezone)) {
			continue
		}
		reachable = append(reachable, shop)
		reachableIDs = append(reachableIDs, shop.ID)
	}

	// Gifts are loaded only for shops the exact zone check kept.
	gifts, err := s.repo.ListPublishedGifts(ctx, reachableIDs, customerType)
	if err != nil {
		return nil, err
	}
	byShop := map[uuid.UUID][]repository.GiftStock{}
	for _, gift := range gifts {
		byShop[gift.ShopID] = append(byShop[gift.ShopID], gift)
	}

	out := &GiftAvailability{
		Latitude:     q.Latitude,
		Longitude:    q.Longitude,
		DeliveryDate: q.DeliveryDate,
		Shops:        []ShopGiftAvailability{},
	}
	for _, shop := range reachable {
		matched := shopAvailability(shop, byShop[shop.ID], q, shopLocalNow(now, shop.Timezone))
		if matched != nil {
			out.Shops = append(out.Shops, *matched)
		}
	}
	return out, nil
}

// zoneReaches is the exact distance check. The SQL box is only a rough pass.
func zoneReaches(shop repository.ShopForAvailability, q GiftAvailabilityQuery, now time.Time) bool {
	opt := buildSellerDeliveryOption(shop.Latitude, shop.Longitude, &q.Latitude, &q.Longitude, shop.Zones, now)
	if opt == nil || !opt.Available {
		return false
	}
	if q.DeliveryDate != "" && opt.EstimatedDeliveryDate > q.DeliveryDate {
		return false
	}
	return true
}

// shopAvailability applies the shop's delivery zones and each gift's stock.
// Nil means the shop cannot deliver the search.
func shopAvailability(shop repository.ShopForAvailability, gifts []repository.GiftStock, q GiftAvailabilityQuery, now time.Time) *ShopGiftAvailability {
	opt := buildSellerDeliveryOption(shop.Latitude, shop.Longitude, &q.Latitude, &q.Longitude, shop.Zones, now)
	if opt == nil || !opt.Available {
		return nil
	}
	// A requested day only counts when the zone can get there in time.
	if q.DeliveryDate != "" && opt.EstimatedDeliveryDate > q.DeliveryDate {
		return nil
	}
	on := q.DeliveryDate
	if on == "" {
		on = opt.EstimatedDeliveryDate
	}

	ids := make([]string, 0, len(gifts))
	products := make([]AvailableProduct, 0, len(gifts))
	for _, gift := range gifts {
		// Same-day only if the gift is ready at or before the cutoff.
		// ready = now + prep_minutes. A later ready time misses today.
		arrival := arrivalWithPrep(now, opt, gift.PrepMinutes)
		if q.DeliveryDate != "" && arrival > q.DeliveryDate {
			continue
		}
		onGift := on
		if arrival != "" {
			onGift = arrival
		}
		if q.DeliveryDate != "" {
			onGift = q.DeliveryDate
		}
		if !giftCanBeSent(gift, onGift) {
			continue
		}
		ids = append(ids, gift.ID.String())
		tags := gift.OccasionTags
		if tags == nil {
			tags = []string{}
		}
		products = append(products, AvailableProduct{
			ID:           gift.ID.String(),
			ShopID:       gift.ShopID.String(),
			Name:         gift.Name,
			Slug:         gift.Slug,
			Description:  gift.Description,
			PriceAmount:  gift.PriceAmount,
			Currency:     gift.Currency,
			ImageURL:     gift.ImageURL,
			OccasionTags: tags,
			Status:       "published",
			StockLeft:    lowStockLeft(gift),
		})
	}
	if len(ids) == 0 {
		return nil
	}
	return &ShopGiftAvailability{
		ShopID:                shop.ID.String(),
		ShopName:              shop.Name,
		DistanceKm:            opt.DistanceKm,
		MaxKm:                 opt.MaxKm,
		PriceAmount:           opt.PriceAmount,
		Currency:              opt.Currency,
		IsFree:                opt.IsFree,
		EstimatedDays:         opt.EstimatedDays,
		CutoffTime:            opt.CutoffTime,
		EstimatedDeliveryDate: opt.EstimatedDeliveryDate,
		ProductIDs:            ids,
		Products:              products,
	}
}

// lowStockLeft is the sellable quantity when it is at or below the threshold.
func lowStockLeft(gift repository.GiftStock) *int {
	if !gift.HasInventory {
		return nil
	}
	sellable := max(gift.AvailableQty-gift.ReservedQty, 0)
	if sellable > gift.LowStockThreshold {
		return nil
	}
	left := sellable
	return &left
}

// shopLocalNow shifts an instant into the shop's IANA timezone.
// An unknown zone keeps the clock that was passed in.
func shopLocalNow(now time.Time, timezone string) time.Time {
	loc, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil || loc == nil {
		return now
	}
	return now.In(loc)
}

// arrivalWithPrep is the earliest day a gift can arrive. On a same-day zone,
// now + prep_minutes after the cutoff misses today, so the gift arrives the
// next day the shop can still dispatch before that cutoff.
func arrivalWithPrep(now time.Time, opt *SellerDeliveryOption, prepMinutes int) string {
	if opt == nil || opt.EstimatedDeliveryDate == "" {
		return ""
	}
	if opt.CutoffTime == "" || prepMinutes <= 0 {
		return opt.EstimatedDeliveryDate
	}
	ready := now.Add(time.Duration(prepMinutes) * time.Minute)
	for day := 0; day < 14; day++ {
		date := now.AddDate(0, 0, day)
		if ready.Format("2006-01-02") > date.Format("2006-01-02") {
			continue
		}
		if date.Format("2006-01-02") == ready.Format("2006-01-02") && pastCutoff(ready, opt.CutoffTime) {
			continue
		}
		return date.Format("2006-01-02")
	}
	return ready.Format("2006-01-02")
}

// giftCanBeSent is false when stock is gone or the day is marked unavailable.
// A gift with no inventory row is still listed: stock was never set.
func giftCanBeSent(gift repository.GiftStock, on string) bool {
	if gift.HasInventory && gift.AvailableQty-gift.ReservedQty <= 0 {
		return false
	}
	if on == "" {
		return true
	}
	for _, blocked := range gift.UnavailableDates {
		if blocked.UTC().Format("2006-01-02") == on {
			return false
		}
	}
	return true
}
