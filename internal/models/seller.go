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
	ImageURL           *string   `json:"image_url,omitempty"`

	LocalName *string `json:"local_name,omitempty"`
	// RegistrationStatus is registered | pending | no_number.
	RegistrationStatus *string `json:"registration_status,omitempty"`
	RegistrationNote   *string `json:"registration_note,omitempty"`
	// TaxStatus is registered | not_registered | unsure.
	TaxStatus *string `json:"tax_status,omitempty"`
	// ContactName and ContactRole describe the person managing the account.
	// ContactRole is owner | director | authorised.
	ContactName          *string    `json:"contact_name,omitempty"`
	ContactRole          *string    `json:"contact_role,omitempty"`
	ContactJobTitle      *string    `json:"contact_job_title,omitempty"`
	AuthorityConfirmedAt *time.Time `json:"authority_confirmed_at,omitempty"`
	TermsAcceptedAt      *time.Time `json:"terms_accepted_at,omitempty"`
	MarketingOptIn       bool       `json:"marketing_opt_in"`

	// EmailVerifiedAt is set once the seller enters the emailed code.
	// Until then sign-in is refused. The code columns are never sent to clients.
	EmailVerifiedAt    *time.Time `json:"email_verified_at,omitempty"`
	EmailCodeHash      *string    `json:"-"`
	EmailCodeExpiresAt *time.Time `json:"-"`
	EmailCodeSentAt    *time.Time `json:"-"`
	EmailCodeAttempts  int        `json:"-"`
}

// AdminSellerSummary is one row on the admin sellers screen.
// Status is active, suspended, or deleted. Password and email codes are omitted.
type AdminSellerSummary struct {
	ID                 uuid.UUID  `json:"id"`
	LegalName          string     `json:"legal_name"`
	TradingName        *string    `json:"trading_name,omitempty"`
	Email              string     `json:"email"`
	Phone              *string    `json:"phone,omitempty"`
	SellerType         string     `json:"seller_type"`
	CountryName        string     `json:"country_name"`
	VerificationStatus string     `json:"verification_status"`
	Status             string     `json:"status"`
	EmailVerifiedAt    *time.Time `json:"email_verified_at,omitempty"`
	ShopCount          int        `json:"shop_count"`
	CreatedAt          time.Time  `json:"created_at"`
}

// SellerIdentifier maps to seller.seller_identifiers.
type SellerIdentifier struct {
	ID              uuid.UUID `json:"id"`
	SellerID        uuid.UUID `json:"seller_id"`
	CountryID       uuid.UUID `json:"country_id"`
	Type            string    `json:"type"`
	Value           string    `json:"value"`
	ValueNormalised string    `json:"-"`
	Authority       *string   `json:"authority,omitempty"`
	Jurisdiction    *string   `json:"jurisdiction,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// SellerTaxRegistration maps to seller.seller_tax_registrations.
type SellerTaxRegistration struct {
	ID           uuid.UUID `json:"id"`
	SellerID     uuid.UUID `json:"seller_id"`
	CountryID    uuid.UUID `json:"country_id"`
	Jurisdiction *string   `json:"jurisdiction,omitempty"`
	Scheme       string    `json:"scheme"`
	Number       string    `json:"number"`
	CreatedAt    time.Time `json:"created_at"`
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
	ID        uuid.UUID `json:"id"`
	SellerID  uuid.UUID `json:"seller_id"`
	CountryID uuid.UUID `json:"country_id"`
	// Timezone is the IANA zone used for same-day cutoffs, for example Asia/Colombo.
	Timezone                string             `json:"timezone"`
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

	Website       *string  `json:"website,omitempty"`
	SupportEmail  *string  `json:"support_email,omitempty"`
	ReturnsPolicy *string  `json:"returns_policy,omitempty"`
	Categories    []string `json:"categories"`
	GiftOptions   []string `json:"gift_options"`
	// WorkingDays holds mon..sun. Stored for the seller's calendar; delivery
	// dates do not read it yet.
	WorkingDays   []string `json:"working_days"`
	PickupEnabled bool     `json:"pickup_enabled"`

	// SellerVerificationStatus is filled on public shop reads. It is the
	// seller's verification_status, not a column on seller.shops.
	SellerVerificationStatus string `json:"seller_verification_status,omitempty"`
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
	// CutoffTime is HH:MM, set only when EstimatedDays is 0 (same day).
	CutoffTime *string   `json:"cutoff_time,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SellerDetails is seller profile with addresses and shops.
type SellerDetails struct {
	Seller
	Addresses        []SellerAddress         `json:"addresses"`
	Shops            []Shop                  `json:"shops"`
	Identifiers      []SellerIdentifier      `json:"identifiers"`
	TaxRegistrations []SellerTaxRegistration `json:"tax_registrations"`
}

// AdminSellerRecord is everything an admin can open for one seller:
// the business profile, shops, every gift, and order lines.
type AdminSellerRecord struct {
	SellerDetails
	CountryName string                   `json:"country_name"`
	Products    []Product                `json:"products"`
	Orders      []SellerOrderItemSummary `json:"orders"`
}
