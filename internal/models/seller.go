package models

import (
	"time"

	"github.com/google/uuid"
)

// Seller maps to seller.sellers.
type Seller struct {
	ID                 uuid.UUID `json:"id"`
	CountryID          uuid.UUID `json:"country_id"`
	SellerType         string    `json:"seller_type"`
	LegalName          string    `json:"legal_name"`
	TradingName        *string   `json:"trading_name,omitempty"`
	Email              string    `json:"email"`
	Phone              *string   `json:"phone,omitempty"`
	PasswordHash       string    `json:"-"`
	VerificationStatus string    `json:"verification_status"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
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
	ID                      uuid.UUID  `json:"id"`
	SellerID                uuid.UUID  `json:"seller_id"`
	Name                    string     `json:"name"`
	Slug                    string     `json:"slug"`
	Description             *string    `json:"description,omitempty"`
	ReturnAddressMode       string     `json:"return_address_mode"`
	CustomerVisibleLocation *string    `json:"customer_visible_location,omitempty"`
	Status                  string     `json:"status"`
	AddressID               *uuid.UUID `json:"address_id,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
	UpdatedAt               time.Time  `json:"updated_at"`
}

// SellerDetails is seller profile with addresses and shops.
type SellerDetails struct {
	Seller
	Addresses []SellerAddress `json:"addresses"`
	Shops     []Shop          `json:"shops"`
}
