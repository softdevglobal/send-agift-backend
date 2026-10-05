package models

import (
	"time"

	"github.com/google/uuid"
)

// SellerApplication is everything a seller tells us when they apply, for an
// admin to review before the account is approved.
type SellerApplication struct {
	SellerID       uuid.UUID                 `json:"seller_id"`
	Business       ApplicationBusiness       `json:"business"`
	Representative ApplicationRepresentative `json:"representative"`
	Addresses      ApplicationAddresses      `json:"addresses"`
	Shop           ApplicationShop           `json:"shop"`
	Fulfilment     ApplicationFulfilment     `json:"fulfilment"`
	Payout         ApplicationPayout         `json:"payout"`
	Consents       ApplicationConsents       `json:"consents"`
	ReviewReasons  []string                  `json:"review_reasons"`
	Document       *ApplicationDocument      `json:"document"`
	SubmittedAt    time.Time                 `json:"submitted_at"`
	UpdatedAt      time.Time                 `json:"updated_at"`
}

// ApplicationBusiness is the legal business: where and how it is registered,
// and its tax registrations.
type ApplicationBusiness struct {
	Country            string               `json:"country"` // ISO 3166-1 alpha-2
	EntityType         string               `json:"entity_type"`
	EntityTypeOther    *string              `json:"entity_type_other"`
	LegalName          string               `json:"legal_name"`
	LocalName          *string              `json:"local_name"`
	TradingName        *string              `json:"trading_name"`
	RegistrationStatus string               `json:"registration_status"` // registered | pending | no_number
	RegistrationNote   *string              `json:"registration_note"`
	Identifiers        []BusinessIdentifier `json:"identifiers"`
	TaxStatus          string               `json:"tax_status"` // registered | not_registered | unsure
	TaxRegistrations   []TaxRegistration    `json:"tax_registrations"`
}

// BusinessIdentifier is a registration number and who issued it.
type BusinessIdentifier struct {
	Type         string  `json:"type"` // e.g. ABN, COMPANY_NUMBER, OTHER
	TypeLabel    *string `json:"type_label"`
	Value        string  `json:"value"`
	Authority    *string `json:"authority"`
	Jurisdiction *string `json:"jurisdiction"`
}

// TaxRegistration is one tax number, in any country.
type TaxRegistration struct {
	Country      string  `json:"country"`
	Jurisdiction *string `json:"jurisdiction"`
	Scheme       string  `json:"scheme"` // VAT, GST, ...
	Number       string  `json:"number"`
}

// ApplicationRepresentative is the person applying for the business.
type ApplicationRepresentative struct {
	FullName           string  `json:"full_name"`
	Role               string  `json:"role"` // owner | director | authorised
	JobTitle           *string `json:"job_title"`
	Language           string  `json:"language"`
	LanguageOther      *string `json:"language_other"`
	AuthorityConfirmed bool    `json:"authority_confirmed"`
}

// ApplicationAddress is an address as written locally, in any country.
type ApplicationAddress struct {
	Country      string   `json:"country"` // ISO alpha-2, or ZZ when not listed
	CountryOther *string  `json:"country_other"`
	Line1        string   `json:"line1"`
	Line2        *string  `json:"line2"`
	City         string   `json:"city"`
	Region       *string  `json:"region"`
	PostalCode   *string  `json:"postal_code"`
	Latitude     *float64 `json:"latitude"`
	Longitude    *float64 `json:"longitude"`
}

// ApplicationAddresses are the registered, pickup and return addresses.
// Pickup and Return are always filled in, copied when marked the same.
type ApplicationAddresses struct {
	Registered             ApplicationAddress `json:"registered"`
	Pickup                 ApplicationAddress `json:"pickup"`
	Return                 ApplicationAddress `json:"return"`
	PickupSameAsRegistered bool               `json:"pickup_same_as_registered"`
	ReturnSameAsPickup     bool               `json:"return_same_as_pickup"`
}

// ApplicationShop is the storefront the seller plans to open.
type ApplicationShop struct {
	DisplayName    string   `json:"display_name"`
	Slug           string   `json:"slug"`
	Description    string   `json:"description"`
	Categories     []string `json:"categories"`
	Website        *string  `json:"website"`
	Currency       string   `json:"currency"`
	TimeZone       string   `json:"time_zone"`
	SupportEmail   *string  `json:"support_email"`
	PublicLocation string   `json:"public_location"` // city_country | country_only | full_pickup
	GiftOptions    []string `json:"gift_options"`
}

// ApplicationFulfilment is how the seller gets gifts to customers.
type ApplicationFulfilment struct {
	DeliveryEnabled     bool           `json:"delivery_enabled"`
	PickupEnabled       bool           `json:"pickup_enabled"`
	Bands               []DeliveryBand `json:"bands"`
	OrderCutoff         *string        `json:"order_cutoff"` // HH:MM in the shop's time zone
	DeliveryNotes       *string        `json:"delivery_notes"`
	WorkingDays         []string       `json:"working_days"`
	PickupInstructions  *string        `json:"pickup_instructions"`
	ReturnsPolicy       string         `json:"returns_policy"`
	CrossBorderInterest bool           `json:"cross_border_interest"`
}

// DeliveryBand is a delivery price and time up to a distance from pickup.
type DeliveryBand struct {
	UpToKm float64 `json:"up_to_km"`
	Fee    float64 `json:"fee"`
	Days   int     `json:"days"`
}

// ApplicationPayout is where the seller would like to be paid.
type ApplicationPayout struct {
	BankCountry      string  `json:"bank_country"`
	BankCountryOther *string `json:"bank_country_other"`
	Currency         string  `json:"currency"`
}

// ApplicationConsents are the confirmations given when applying.
type ApplicationConsents struct {
	DetailsConfirmed bool `json:"details_confirmed"`
	TermsAccepted    bool `json:"terms_accepted"`
	MarketingOptIn   bool `json:"marketing_opt_in"`
}

// ApplicationDocument is the business registration evidence, kept private.
type ApplicationDocument struct {
	Name        string    `json:"name"`
	ContentType string    `json:"content_type"`
	UploadedAt  time.Time `json:"uploaded_at"`
}

// AdminSellerDetails is a seller with their application, for review.
type AdminSellerDetails struct {
	SellerDetails
	Application *SellerApplication `json:"application"`
}
