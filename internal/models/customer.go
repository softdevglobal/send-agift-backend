package models

import (
	"time"

	"github.com/google/uuid"
)

// Customer maps to customer.customers.
type Customer struct {
	ID                 uuid.UUID  `json:"id"`
	CountryID          uuid.UUID  `json:"country_id"`
	Email              string     `json:"email"`
	Phone              *string    `json:"phone,omitempty"`
	PasswordHash       string     `json:"-"`
	DisplayName        *string    `json:"display_name,omitempty"`
	CustomerType       string     `json:"customer_type"`
	DateOfBirth        *time.Time `json:"date_of_birth,omitempty"`
	AgeVerifiedAt      *time.Time `json:"age_verified_at,omitempty"`
	IdentityVerifiedAt *time.Time `json:"identity_verified_at,omitempty"`
	Status             string     `json:"status"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	DeletedAt          *time.Time `json:"deleted_at,omitempty"`
	ImageURL           *string    `json:"image_url,omitempty"`
	// True for an account made for a gift recipient, until they replace the
	// default password it started with.
	PasswordChangeRequired bool       `json:"password_change_required"`
	PhoneVerifiedAt        *time.Time `json:"phone_verified_at,omitempty"`
}

// CustomerAddress maps to customer.customer_addresses.
type CustomerAddress struct {
	ID          uuid.UUID `json:"id"`
	CustomerID  uuid.UUID `json:"customer_id"`
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

type SavedGift struct {
	ID         uuid.UUID `json:"id"`
	CustomerID uuid.UUID `json:"customer_id"`
	ProductID  uuid.UUID `json:"product_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// SavedGiftDetails is a saved gift with full product details from seller.products.
type SavedGiftDetails struct {
	ID         uuid.UUID `json:"id"`
	CustomerID uuid.UUID `json:"customer_id"`
	ProductID  uuid.UUID `json:"product_id"`
	CreatedAt  time.Time `json:"created_at"`
	Product    Product   `json:"product"`
}

// CustomerDetails is a customer profile with all addresses.
type CustomerDetails struct {
	Customer
	Addresses []CustomerAddress `json:"addresses"`
}

// AdminCustomerRecord is everything an admin can open for one customer:
// the profile, recipients, saved gifts, and orders.
type AdminCustomerRecord struct {
	CustomerDetails
	CountryName string             `json:"country_name"`
	Recipients  []RecipientDetails `json:"recipients"`
	SavedGifts  []SavedGiftDetails `json:"saved_gifts"`
	Orders      []Order            `json:"orders"`
}
