package services

import (
	"errors"
	"fmt"
	"math"
	"time"

	"myapp/internal/models"
)

// ErrOutsideDeliveryZone: the recipient is farther than the shop's largest delivery zone.
var ErrOutsideDeliveryZone = errors.New("recipient is outside the shop's delivery zones")

// SellerDeliveryModeName is the mode value clients send/receive for seller delivery.
const SellerDeliveryModeName = "seller_delivery"

// SellerDeliveryOption is the shop's own delivery for one recipient, priced from
// the shop's delivery zones by straight-line distance.
type SellerDeliveryOption struct {
	Mode      string `json:"mode"` // always "seller_delivery"
	Available bool   `json:"available"`
	// Reason explains why Available is false (no zones, missing coordinates, too far).
	Reason                string   `json:"reason,omitempty"`
	DistanceKm            *float64 `json:"distance_km,omitempty"`
	MaxKm                 float64  `json:"max_km,omitempty"`      // matched zone
	FarthestKm            float64  `json:"farthest_km,omitempty"` // shop's largest zone
	PriceAmount           int      `json:"price_amount"`          // minor units; 0 = free
	Currency              string   `json:"currency,omitempty"`
	IsFree                bool     `json:"is_free"`
	EstimatedDays         int      `json:"estimated_days"`                    // 0 = same day
	EstimatedDeliveryDate string   `json:"estimated_delivery_date,omitempty"` // YYYY-MM-DD
}

// hasZoneAndPoints is true when a distance check is meaningful (both points and zones exist).
func (o *SellerDeliveryOption) hasZoneAndPoints() bool {
	return o != nil && o.DistanceKm != nil && o.FarthestKm > 0
}

// buildSellerDeliveryOption picks the smallest zone whose max_km covers the
// distance from the shop to the recipient. zones must be sorted by max_km asc.
func buildSellerDeliveryOption(fromLat, fromLng, toLat, toLng *float64, zones []models.ShopDeliveryZone, now time.Time) *SellerDeliveryOption {
	opt := &SellerDeliveryOption{Mode: SellerDeliveryModeName}
	if len(zones) == 0 {
		opt.Reason = "shop has no delivery zones"
		return opt
	}
	opt.FarthestKm = zones[len(zones)-1].MaxKm
	if fromLat == nil || fromLng == nil {
		opt.Reason = "shop location (latitude/longitude) is not set"
		return opt
	}
	if toLat == nil || toLng == nil {
		opt.Reason = "recipient address has no latitude/longitude"
		return opt
	}
	km := math.Round(haversineKm(*fromLat, *fromLng, *toLat, *toLng)*100) / 100
	opt.DistanceKm = &km
	for _, z := range zones {
		if km <= z.MaxKm {
			opt.Available = true
			opt.MaxKm = z.MaxKm
			opt.PriceAmount = z.PriceAmount
			opt.Currency = z.Currency
			opt.IsFree = z.PriceAmount == 0
			opt.EstimatedDays = z.EstimatedDays
			opt.EstimatedDeliveryDate = now.AddDate(0, 0, z.EstimatedDays).Format("2006-01-02")
			return opt
		}
	}
	opt.Reason = fmt.Sprintf("recipient is %.2f km away; the farthest delivery zone is %.2f km", km, opt.FarthestKm)
	return opt
}

// applyToShipment copies the zone snapshot onto a shipment row.
func (o *SellerDeliveryOption) applyToShipment(s *models.Shipment) {
	if o == nil {
		return
	}
	s.DistanceKm = o.DistanceKm
	if !o.Available {
		return
	}
	maxKm := o.MaxKm
	price := o.PriceAmount
	currency := o.Currency
	days := o.EstimatedDays
	s.ZoneMaxKm = &maxKm
	s.PriceAmount = &price
	s.Currency = &currency
	s.EstimatedDays = &days
	if d, err := time.Parse("2006-01-02", o.EstimatedDeliveryDate); err == nil {
		s.EstimatedDeliveryDate = &d
	}
}

// haversineKm is the great-circle distance between two points in kilometres.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusKm = 6371.0
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLng := toRad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}
