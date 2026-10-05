package models

import (
	"time"

	"github.com/google/uuid"
)

// Seller maps to seller.sellers.
type Seller struct {
	ID           uuid.UUID `json:"id"`
	CountryID    uuid.UUID `json:"country_id"`
	SellerType   string    `json:"seller_type"`
	LegalName    string    `json:"legal_name"`
	TradingName  *string   `json:"trading_name,omitempty"`
	Email        string    `json:"email"`
	Phone        *string   `json:"phone,omitempty"`
	PasswordHash string    `json:"-"`
	// unverified (email not confirmed yet) -> pending (waiting for an admin)
	// -> verified | rejected.
	VerificationStatus string     `json:"verification_status"`
	Status             string     `json:"status"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ImageURL           *string    `json:"image_url,omitempty"`
	EmailVerifiedAt    *time.Time `json:"email_verified_at,omitempty"`
	// What the admin told the seller when approving or rejecting them.
	VerificationNote       *string    `json:"verification_note,omitempty"`
	VerificationReviewedAt *time.Time `json:"verification_reviewed_at,omitempty"`
}

// AdminSellerSummary is one row of the admin seller review list.
type AdminSellerSummary struct {
	Seller
	CountryName string  `json:"country_name"`
	ShopCount   int     `json:"shop_count"`
	City        *string `json:"city,omitempty"`
}

// SellerAddress maps to seller.seller_addresses.
type SellerAddress struct {
	ID          uuid.UUID `json:"id"`
	SellerID    uuid.UUID `json:"seller_id"`
	CountryID   uuid.UUID `json:"country_id"`
	Label       *string   `json:"label,omitempty"`
	AddressType string    `json:"address_type"`
	Line1       string    `json:"line1"`
	Line2       *string   `json:"line2,omitempty"`
	City        string    `json:"city"`
	Region      *string   `json:"region,omitempty"`
	PostalCode  *string   `json:"postal_code,omitempty"`
	Latitude    *float64  `json:"latitude,omitempty"`
	Longitude   *float64  `json:"longitude,omitempty"`
	IsDefault   bool      `json:"is_default"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Shop maps to seller.shops.
type Shop struct {
	ID                      uuid.UUID          `json:"id"`
	SellerID                uuid.UUID          `json:"seller_id"`
	CountryID               uuid.UUID          `json:"country_id"`
	Name                    string             `json:"name"`
	Slug                    string             `json:"slug"`
	Description             *string            `json:"description,omitempty"`
	CustomerVisibleLocation *string            `json:"customer_visible_location,omitempty"`
	Status                  string             `json:"status"`
	AddressID               *uuid.UUID         `json:"address_id,omitempty"`
	ReturnAddressID         *uuid.UUID         `json:"return_address_id,omitempty"`
	CreatedAt               time.Time          `json:"created_at"`
	UpdatedAt               time.Time          `json:"updated_at"`
	ImageURL                *string            `json:"image_url,omitempty"`
	Latitude                *float64           `json:"latitude,omitempty"`
	Longitude               *float64           `json:"longitude,omitempty"`
	DeliveryZones           []ShopDeliveryZone `json:"delivery_zones"`
}

// ShopDeliveryZone is one local-delivery distance band (max_km → price).
// PriceAmount 0 means free (IsFree is derived, not stored).
type ShopDeliveryZone struct {
	ID            uuid.UUID `json:"id"`
	ShopID        uuid.UUID `json:"shop_id"`
	MaxKm         float64   `json:"max_km"`
	PriceAmount   int       `json:"price_amount"`
	Currency      string    `json:"currency"`
	IsFree        bool      `json:"is_free"`
	EstimatedDays int       `json:"estimated_days"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SellerDetails is seller profile with addresses and shops.
type SellerDetails struct {
	Seller
	Addresses []SellerAddress `json:"addresses"`
	Shops     []Shop          `json:"shops"`
}
