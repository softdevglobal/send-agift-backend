package models

import (
	"time"

	"github.com/google/uuid"
)

// Country maps to core.countries.
type Country struct {
	ID               uuid.UUID `json:"id"`
	ISOCode          string    `json:"iso_code"`
	Name             string    `json:"name"`
	DefaultCurrency  string    `json:"default_currency"`
	DefaultTimezone  string    `json:"default_timezone"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
