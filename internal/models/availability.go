package models

import "github.com/google/uuid"

// AvailabilityDestination is the place a shopper typed on the gifts search bar.
// Coordinates come from the place lookup so delivery zones can be measured.
type AvailabilityDestination struct {
	Name       string   `json:"name,omitempty"`
	Line1      string   `json:"line1"`
	Line2      string   `json:"line2,omitempty"`
	City       string   `json:"city"`
	Region     string   `json:"region,omitempty"`
	PostalCode string   `json:"postal_code,omitempty"`
	Country    string   `json:"country"`
	Latitude   *float64 `json:"latitude,omitempty"`
	Longitude  *float64 `json:"longitude,omitempty"`
}

// AvailabilityInput is the body of POST /shipping/availability.
type AvailabilityInput struct {
	DeliveryDate string                  `json:"delivery_date"`
	Destination  AvailabilityDestination `json:"destination"`
}

// AvailabilityShop is one active shop plus a single published product used as
// the sample parcel for a Shippo rate. Zones are that shop's delivery bands.
type AvailabilityShop struct {
	ShopID             uuid.UUID
	ShopName           string
	ProductID          uuid.UUID
	ParcelLength       *string
	ParcelWidth        *string
	ParcelHeight       *string
	ParcelDistanceUnit *string
	ParcelWeight       *string
	ParcelMassUnit     *string
	FromName           string
	Street1            string
	Street2            string
	City               string
	Region             string
	PostalCode         string
	CountryISO         string
	Latitude           *float64
	Longitude          *float64
	Zones              []ShopDeliveryZone
}

// ShopAvailability is whether one shop can reach the destination by the date.
// Available shops are shown in full: every published product, not only the sample.
type ShopAvailability struct {
	ShopID          string `json:"shop_id"`
	ShopName        string `json:"shop_name"`
	Available       bool   `json:"available"`
	Mode            string `json:"mode,omitempty"`
	EstimatedDays   int    `json:"estimated_days,omitempty"`
	SampleProductID string `json:"sample_product_id,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// AvailabilityResult is the list of shops checked for this address and date.
type AvailabilityResult struct {
	Shops []ShopAvailability `json:"shops"`
}
