package services

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"myapp/internal/models"
)

// ErrOutsideDeliveryZone: the recipient is farther than the shop's largest delivery zone.
var ErrOutsideDeliveryZone = errors.New("recipient is outside the shop's delivery zones")

// DeliveryZoneError is a shop that cannot deliver to the order's address,
// with the reason in words a shopper can read. It is an ErrOutsideDeliveryZone.
type DeliveryZoneError struct {
	ShopName   string
	ReasonCode string
	Reason     string
}

func (e *DeliveryZoneError) Error() string {
	shop := e.ShopName
	if shop == "" {
		shop = "This shop"
	}
	if e.Reason == "" {
		return shop + " cannot deliver to this address."
	}
	return shop + " cannot deliver to this address. " + e.Reason + "."
}

func (e *DeliveryZoneError) Unwrap() error { return ErrOutsideDeliveryZone }

// SellerDeliveryModeName is the mode value clients send/receive for seller delivery.
const SellerDeliveryModeName = "seller_delivery"

// SellerDeliveryOption is the shop's own delivery for one recipient, priced from
// the shop's delivery zones by straight-line distance.
type SellerDeliveryOption struct {
	Mode      string `json:"mode"` // always "seller_delivery"
	Available bool   `json:"available"`
	// Reason explains why Available is false, in words a shopper can read.
	Reason string `json:"reason,omitempty"`
	// ReasonCode is the same, for apps: NO_ZONES, SHOP_NO_LOCATION,
	// ADDRESS_NO_LOCATION or OUTSIDE_ZONE.
	ReasonCode            string   `json:"reason_code,omitempty"`
	DistanceKm            *float64 `json:"distance_km,omitempty"`
	MaxKm                 float64  `json:"max_km,omitempty"`      // matched zone
	FarthestKm            float64  `json:"farthest_km,omitempty"` // shop's largest zone
	PriceAmount           int      `json:"price_amount"`          // minor units; 0 = free
	Currency              string   `json:"currency,omitempty"`
	IsFree                bool     `json:"is_free"`
	EstimatedDays         int      `json:"estimated_days"`                    // 0 = same day
	CutoffTime            string   `json:"cutoff_time,omitempty"`             // HH:MM when the zone is same-day
	EstimatedDeliveryDate string   `json:"estimated_delivery_date,omitempty"` // YYYY-MM-DD

	// localNow is the shop's wall clock the option was built with. Delivery-date
	// checks need the same "now" the zone's cutoff was judged against.
	localNow time.Time
	// shopName is only for messages that name the shop.
	shopName string
}

// hasZoneAndPoints is true when a distance check is meaningful (both points and zones exist).
func (o *SellerDeliveryOption) hasZoneAndPoints() bool {
	return o != nil && o.DistanceKm != nil && o.FarthestKm > 0
}

// buildSellerDeliveryOption picks the smallest zone whose max_km covers the
// distance from the shop to the recipient. zones must be sorted by max_km asc.
func buildSellerDeliveryOption(fromLat, fromLng, toLat, toLng *float64, zones []models.ShopDeliveryZone, now time.Time) *SellerDeliveryOption {
	opt := &SellerDeliveryOption{Mode: SellerDeliveryModeName, localNow: now}
	if len(zones) == 0 {
		opt.ReasonCode = "NO_ZONES"
		opt.Reason = "This shop has not set up delivery yet"
		return opt
	}
	opt.FarthestKm = zones[len(zones)-1].MaxKm
	if fromLat == nil || fromLng == nil {
		opt.ReasonCode = "SHOP_NO_LOCATION"
		opt.Reason = "This shop has not set its location, so delivery cannot be worked out"
		return opt
	}
	if toLat == nil || toLng == nil {
		opt.ReasonCode = "ADDRESS_NO_LOCATION"
		opt.Reason = "This address is not pinned on the map, so the distance cannot be measured"
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
			days := z.EstimatedDays
			if z.CutoffTime != nil {
				opt.CutoffTime = *z.CutoffTime
			}
			// After the same-day cutoff, the order cannot leave today.
			if days == 0 && pastCutoff(now, opt.CutoffTime) {
				days = 1
			}
			opt.EstimatedDays = days
			opt.EstimatedDeliveryDate = now.AddDate(0, 0, days).Format("2006-01-02")
			return opt
		}
	}
	opt.ReasonCode = "OUTSIDE_ZONE"
	opt.Reason = fmt.Sprintf("This address is %s km away. The shop delivers up to %s km",
		friendlyKm(km), friendlyKm(opt.FarthestKm))
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

// pastCutoff is true when now is later than HH:MM on the same clock.
func pastCutoff(now time.Time, cutoff string) bool {
	parsed, err := time.Parse("15:04", strings.TrimSpace(cutoff))
	if err != nil {
		return false
	}
	nowMins := now.Hour()*60 + now.Minute()
	cutMins := parsed.Hour()*60 + parsed.Minute()
	return nowMins > cutMins
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

// friendlyKm writes a distance the way a person reads it: whole kilometres
// with thousands separators past 10 km ("3,214"), one decimal below ("2.4").
func friendlyKm(km float64) string {
	if km < 10 {
		return strconv.FormatFloat(math.Round(km*10)/10, 'f', -1, 64)
	}
	digits := strconv.FormatInt(int64(math.Round(km)), 10)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
