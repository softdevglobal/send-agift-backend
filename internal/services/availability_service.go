package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// ErrAvailabilityDestination means the search bar did not send a place Shippo
// and the delivery zones can both use.
var ErrAvailabilityDestination = errors.New("destination needs a street, city, country, and map coordinates")

// ErrAvailabilityDate means delivery_date was sent but is not YYYY-MM-DD.
var ErrAvailabilityDate = errors.New("delivery_date must be YYYY-MM-DD")

const (
	availabilityShippoConcurrency = 5
	availabilityCacheTTL          = 15 * time.Minute
)

// AvailabilityService decides which shops can deliver to one address by one date.
// A matching delivery zone covers the whole shop. Otherwise one published product
// from that shop is quoted with Shippo, and that answer stands for every gift there.
type AvailabilityService struct {
	repo   *repository.AvailabilityRepository
	shippo *ShippoClient

	mu    sync.Mutex
	cache map[string]availabilityCacheEntry
}

type availabilityCacheEntry struct {
	available     bool
	estimatedDays int
	reason        string
	expires       time.Time
}

func NewAvailabilityService(repo *repository.AvailabilityRepository, shippo *ShippoClient) *AvailabilityService {
	return &AvailabilityService{
		repo:   repo,
		shippo: shippo,
		cache:  map[string]availabilityCacheEntry{},
	}
}

// Check returns one row per active shop that has a published gift.
func (s *AvailabilityService) Check(ctx context.Context, in models.AvailabilityInput) (*models.AvailabilityResult, error) {
	dest := normalizeAvailabilityDestination(in.Destination)
	if dest.Line1 == "" || dest.City == "" || dest.Country == "" || dest.Latitude == nil || dest.Longitude == nil {
		return nil, ErrAvailabilityDestination
	}
	deliverBy := parseDeliveryDate(in.DeliveryDate)
	if strings.TrimSpace(in.DeliveryDate) != "" && deliverBy.IsZero() {
		return nil, ErrAvailabilityDate
	}

	shops, err := s.repo.ListShopSamples(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	results := make([]models.ShopAvailability, len(shops))
	var wg sync.WaitGroup
	sem := make(chan struct{}, availabilityShippoConcurrency)

	for i := range shops {
		shop := shops[i]
		opt := buildSellerDeliveryOption(shop.Latitude, shop.Longitude, dest.Latitude, dest.Longitude, shop.Zones, now)
		if sellerDeliveryMeetsDate(opt, deliverBy) {
			results[i] = models.ShopAvailability{
				ShopID:          shop.ShopID.String(),
				ShopName:        shop.ShopName,
				Available:       true,
				Mode:            SellerDeliveryModeName,
				EstimatedDays:   opt.EstimatedDays,
				SampleProductID: shop.ProductID.String(),
			}
			continue
		}

		wg.Add(1)
		go func(i int, shop models.AvailabilityShop, zoneReason string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = unavailableShop(shop, "delivery check was cancelled")
				return
			}
			results[i] = s.courierAvailability(ctx, shop, dest, deliverBy, zoneReason)
		}(i, shop, zoneReason(opt))
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &models.AvailabilityResult{Shops: results}, nil
}

func (s *AvailabilityService) courierAvailability(
	ctx context.Context,
	shop models.AvailabilityShop,
	dest models.AvailabilityDestination,
	deliverBy time.Time,
	zoneReason string,
) models.ShopAvailability {
	if shop.Street1 == "" || shop.City == "" || shop.CountryISO == "" {
		reason := zoneReason
		if reason == "" {
			reason = "shop has no dispatch address"
		}
		return unavailableShop(shop, reason)
	}
	if s.shippo == nil || !s.shippo.Enabled() {
		reason := zoneReason
		if reason == "" {
			reason = "no courier is available for this route"
		}
		return unavailableShop(shop, reason)
	}

	parcel := sampleParcel(shop)
	key := availabilityCacheKey(shop, dest, deliverBy, parcel)
	if hit, ok := s.cached(key); ok {
		row := unavailableShop(shop, hit.reason)
		row.Available = hit.available
		if hit.available {
			row.Mode = "courier"
			row.EstimatedDays = hit.estimatedDays
			row.Reason = ""
		}
		return row
	}

	from := ShippoAddressInput{
		Name:    firstNonEmpty(shop.FromName, shop.ShopName),
		Street1: shop.Street1,
		Street2: shop.Street2,
		City:    shop.City,
		State:   shop.Region,
		Zip:     shop.PostalCode,
		Country: normalizeCountryISO(shop.CountryISO),
	}
	to := ShippoAddressInput{
		Name:          firstNonEmpty(dest.Name, "Recipient"),
		Street1:       dest.Line1,
		Street2:       dest.Line2,
		City:          dest.City,
		State:         dest.Region,
		Zip:           dest.PostalCode,
		Country:       normalizeCountryISO(dest.Country),
		IsResidential: true,
	}

	shipment, err := s.shippo.CreateShipment(ctx, from, to, parcelToShippo(parcel), "")
	if err != nil || shipment == nil || len(shipment.Rates) == 0 {
		reason := "no courier is available for this route"
		s.store(key, false, 0, reason)
		return unavailableShop(shop, reason)
	}
	best, missed := pickBestRate(mapShippoRates(shipment.Rates), deliverBy)
	if best == nil || missed {
		reason := "no courier can arrive by the selected date"
		if deliverBy.IsZero() {
			reason = "no courier is available for this route"
		}
		s.store(key, false, 0, reason)
		return unavailableShop(shop, reason)
	}
	s.store(key, true, best.EstimatedDays, "")
	return models.ShopAvailability{
		ShopID:          shop.ShopID.String(),
		ShopName:        shop.ShopName,
		Available:       true,
		Mode:            "courier",
		EstimatedDays:   best.EstimatedDays,
		SampleProductID: shop.ProductID.String(),
	}
}

func (s *AvailabilityService) cached(key string) (availabilityCacheEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hit, ok := s.cache[key]
	if !ok || time.Now().After(hit.expires) {
		delete(s.cache, key)
		return availabilityCacheEntry{}, false
	}
	return hit, true
}

func (s *AvailabilityService) store(key string, available bool, days int, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[key] = availabilityCacheEntry{
		available:     available,
		estimatedDays: days,
		reason:        reason,
		expires:       time.Now().Add(availabilityCacheTTL),
	}
}

func unavailableShop(shop models.AvailabilityShop, reason string) models.ShopAvailability {
	return models.ShopAvailability{
		ShopID:          shop.ShopID.String(),
		ShopName:        shop.ShopName,
		Available:       false,
		SampleProductID: shop.ProductID.String(),
		Reason:          reason,
	}
}

func zoneReason(opt *SellerDeliveryOption) string {
	if opt == nil {
		return ""
	}
	return opt.Reason
}

func sellerDeliveryMeetsDate(opt *SellerDeliveryOption, deliverBy time.Time) bool {
	if opt == nil || !opt.Available {
		return false
	}
	if deliverBy.IsZero() {
		return true
	}
	arrival, err := time.Parse("2006-01-02", opt.EstimatedDeliveryDate)
	if err != nil {
		return false
	}
	target := time.Date(deliverBy.Year(), deliverBy.Month(), deliverBy.Day(), 0, 0, 0, 0, time.UTC)
	got := time.Date(arrival.Year(), arrival.Month(), arrival.Day(), 0, 0, 0, 0, time.UTC)
	return !got.After(target)
}

func sampleParcel(shop models.AvailabilityShop) ParcelInput {
	parcel := models.ProductParcelFromNullable(
		shop.ParcelLength, shop.ParcelWidth, shop.ParcelHeight,
		shop.ParcelDistanceUnit, shop.ParcelWeight, shop.ParcelMassUnit,
	)
	if parcel == nil || parcel.Length == "" || parcel.Width == "" || parcel.Height == "" || parcel.Weight == "" {
		return defaultDomesticParcel()
	}
	out := ParcelInput{
		Length: parcel.Length, Width: parcel.Width, Height: parcel.Height,
		DistanceUnit: parcel.DistanceUnit, Weight: parcel.Weight, MassUnit: parcel.MassUnit,
	}
	if out.DistanceUnit == "" {
		out.DistanceUnit = "cm"
	}
	if out.MassUnit == "" {
		out.MassUnit = "kg"
	}
	return out
}

func normalizeAvailabilityDestination(in models.AvailabilityDestination) models.AvailabilityDestination {
	in.Line1 = strings.TrimSpace(in.Line1)
	in.Line2 = strings.TrimSpace(in.Line2)
	in.City = strings.TrimSpace(in.City)
	in.Region = strings.TrimSpace(in.Region)
	in.PostalCode = strings.TrimSpace(in.PostalCode)
	in.Country = normalizeCountryISO(in.Country)
	in.Name = strings.TrimSpace(in.Name)
	return in
}

func availabilityCacheKey(shop models.AvailabilityShop, dest models.AvailabilityDestination, deliverBy time.Time, parcel ParcelInput) string {
	date := ""
	if !deliverBy.IsZero() {
		date = deliverBy.UTC().Format("2006-01-02")
	}
	return fmt.Sprintf("%s|%s|%s|%s|%.4f|%.4f|%s|%s|%s|%s|%s|%s",
		shop.ShopID.String(),
		normalizeCountryISO(shop.CountryISO),
		dest.Country,
		dest.PostalCode,
		valueOrZero(dest.Latitude),
		valueOrZero(dest.Longitude),
		date,
		parcel.Length, parcel.Width, parcel.Height, parcel.Weight, parcel.MassUnit,
	)
}

func valueOrZero(n *float64) float64 {
	if n == nil {
		return 0
	}
	return *n
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
