package models

import (
	"time"

	"github.com/google/uuid"
)

// Order kinds on marketplace.orders.
const (
	OrderKindPaid     = "paid"
	OrderKindFreeGift = "free_gift"
)

// Free gift program states. "not_set" is never stored: it is what a seller
// who has not set a budget yet reads back.
const (
	FreeGiftProgramActive  = "active"
	FreeGiftProgramPaused  = "paused"
	FreeGiftProgramStopped = "stopped" // by an admin
	FreeGiftProgramNotSet  = "not_set"
)

// FreeGiftProgram maps to promo.free_gift_programs: one seller's marketing
// budget for free gifts. Amounts are minor units of Currency.
type FreeGiftProgram struct {
	SellerID    uuid.UUID `json:"seller_id"`
	Status      string    `json:"status"`
	Currency    string    `json:"currency"`
	BudgetCap   int64     `json:"budget_cap"`
	BudgetHeld  int64     `json:"budget_held"`
	BudgetSpent int64     `json:"budget_spent"`
	// BudgetRemaining is cap - held - spent.
	BudgetRemaining int64      `json:"budget_remaining"`
	TermsVersion    *string    `json:"terms_version,omitempty"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// FreeGiftProduct maps to promo.free_gift_products, with enough of the
// product for the seller's list.
type FreeGiftProduct struct {
	ProductID  uuid.UUID `json:"product_id"`
	SellerID   uuid.UUID `json:"seller_id"`
	Status     string    `json:"status"`
	GiftsCap   *int      `json:"gifts_cap"`
	GiftsCount int       `json:"gifts_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`

	ProductName     string  `json:"product_name"`
	ProductImageURL *string `json:"product_image_url,omitempty"`
	ProductStatus   string  `json:"product_status"`
	PriceAmount     int     `json:"price_amount"`
	Currency        string  `json:"currency"`
	ShopName        string  `json:"shop_name"`
	// StockLeft is available minus reserved; nil when stock was never set.
	StockLeft *int `json:"stock_left"`
}

// FreeGiftOffer is a product a customer can send as their free gift right now.
type FreeGiftOffer struct {
	ProductID   uuid.UUID `json:"product_id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description *string   `json:"description,omitempty"`
	ImageURL    *string   `json:"image_url,omitempty"`
	PriceAmount int       `json:"price_amount"`
	Currency    string    `json:"currency"`
	ShopID      uuid.UUID `json:"shop_id"`
	ShopName    string    `json:"shop_name"`
	ShopSlug    string    `json:"shop_slug"`
	CountryID   uuid.UUID `json:"country_id"`
}

// FreeGiftRecipientStatus says whether one recipient can still be sent a
// free gift. Reason is SELF_GIFT, RECIPIENT_CONTACT_REQUIRED or
// RECIPIENT_ALREADY_USED.
type FreeGiftRecipientStatus struct {
	ID         uuid.UUID `json:"id"`
	CanReceive bool      `json:"can_receive"`
	Reason     *string   `json:"reason"`
}

// FreeGiftStatus is what the customer app shows: whether the free gift is
// still there, and what stands in the way of sending it.
type FreeGiftStatus struct {
	Available bool `json:"available"`
	Used      bool `json:"used"`
	// Missing holds "email", "phone" or both when they are not verified.
	Missing []string `json:"missing"`
	// Locked is the hold after an email change.
	Locked    bool                     `json:"locked"`
	Recipient *FreeGiftRecipientStatus `json:"recipient,omitempty"`
}

// AdminFreeGiftProgram is one seller's program as an admin lists it.
type AdminFreeGiftProgram struct {
	FreeGiftProgram
	SellerName  string `json:"seller_name"`
	SellerEmail string `json:"seller_email"`
	Products    int    `json:"products"`
	GiftsSent   int    `json:"gifts_sent"`
}
