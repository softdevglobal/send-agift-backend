package models

import (
	"time"

	"github.com/google/uuid"
)

// Admin maps to admin.admin_users.
type Admin struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"` // never expose in API responses
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"` // NEW
	MFARequired  bool      `json:"mfa_required"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
