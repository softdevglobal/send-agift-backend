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
	BudgetRemaining int64 `json:"budget_remaining"`
	// SellerAgreed is whether the seller has agreed to the marketing budget
	// SendAGift sets for them. Sellers who registered before the agreement
	// existed have to agree from their dashboard.
	SellerAgreed bool       `json:"seller_agreed"`
	TermsVersion *string    `json:"terms_version,omitempty"`
	AcceptedAt   *time.Time `json:"accepted_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
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

// AdminFreeGiftOrder is one free gift that was sent, as an admin reads it:
// the seller who funded it, the customer who sent it, who it went to, what it
// was, and what it cost the seller's budget. Amounts are minor units of Currency.
type AdminFreeGiftOrder struct {
	OrderID     uuid.UUID `json:"order_id"`
	OrderNumber string    `json:"order_number"`
	Status      string    `json:"status"`
	// BudgetStatus is held (not delivered yet), spent (delivered) or released
	// (cancelled, so the budget was given back). Only the product price is
	// held and spent; the customer pays the delivery.
	BudgetStatus  string    `json:"budget_status"`
	CreatedAt     time.Time `json:"created_at"`
	DeliveryDate  string    `json:"delivery_date"`
	Currency      string    `json:"currency"`
	// Cost is what it cost the seller's budget: the product price.
	Cost          int       `json:"cost"`
	ProductAmount int       `json:"product_amount"`
	// DeliveryAmount is the delivery the customer paid.
	DeliveryAmount int `json:"delivery_amount"`
	// DeliveryPaid is whether the customer's delivery payment went through
	// (true too when there was no delivery to pay).
	DeliveryPaid bool `json:"delivery_paid"`

	SellerID   uuid.UUID `json:"seller_id"`
	SellerName string    `json:"seller_name"`
	ShopName   string    `json:"shop_name"`

	CustomerID    uuid.UUID `json:"customer_id"`
	CustomerName  string    `json:"customer_name"`
	CustomerEmail string    `json:"customer_email"`

	RecipientName *string `json:"recipient_name,omitempty"`
	RecipientCity *string `json:"recipient_city,omitempty"`

	ProductID       uuid.UUID `json:"product_id"`
	ProductName     string    `json:"product_name"`
	ProductImageURL *string   `json:"product_image_url,omitempty"`
}

// FreeGiftCountryBudget maps to promo.free_gift_country_budgets: the budget
// an admin set for every seller in one country. BudgetCap is per seller.
type FreeGiftCountryBudget struct {
	CountryID   uuid.UUID `json:"country_id"`
	CountryName string    `json:"country_name"`
	Currency    string    `json:"currency"`
	BudgetCap   int64     `json:"budget_cap"`
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Sellers is every live seller registered in the country; Agreed is how
	// many of them have agreed to the budget, and so have it.
	Sellers int `json:"sellers"`
	Agreed  int `json:"agreed"`
}

// FreeGiftCountryApply says what setting a country budget did.
type FreeGiftCountryApply struct {
	Budget  FreeGiftCountryBudget `json:"budget"`
	Applied int                   `json:"applied"`
	// Skipped sellers, and why: they have not agreed, an admin stopped their
	// program, the amount is under what they already hold and spent, or their
	// program is in another currency.
	NotAgreed      int `json:"skipped_not_agreed"`
	Stopped        int `json:"skipped_stopped"`
	BelowCommitted int `json:"skipped_below_committed"`
	CurrencyLocked int `json:"skipped_currency_locked"`
}
