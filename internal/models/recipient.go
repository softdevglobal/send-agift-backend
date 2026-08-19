package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Recipient maps to customer.recipients.
// Recipient is the core "gift recipient" recode-one row per person.
type Recipient struct {
	ID               uuid.UUID       `json:"id"`
	CustomerID       uuid.UUID       `json:"customer_id"`
	Name             string          `json:"name"`
	Relationship     *string         `json:"relationship,omitempty"`
	Email            *string         `json:"email,omitempty"`
	Phone            *string         `json:"phone,omitempty"`
	ImageURL         *string         `json:"image_url,omitempty"`
	DefaultAddressID *uuid.UUID      `json:"default_address_id,omitempty"`
	Preferences      json.RawMessage `json:"preferences"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// RecipientAddress maps to customer.recipient_addresses.
// Managed via:
//   POST   /customers/me/recipients/{id}/addresses
//   PUT    /customers/me/recipients/{id}/addresses/{addressId}
//   DELETE /customers/me/recipients/{id}/addresses/{addressId}
// Deleting the default address clears recipients.default_address_id (ON DELETE SET NULL).
type RecipientAddress struct {
	ID          uuid.UUID `json:"id"`
	RecipientID uuid.UUID `json:"recipient_id"`
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

// RecipientDetails is a recipient with all of their addresses.
type RecipientDetails struct {
	Recipient
	Addresses []RecipientAddress `json:"addresses"`
}
