package models

import (
	"time"

	"github.com/google/uuid"
)

// Game entry ledger entry types.
const (
	EntryPurchaseGrant    = "purchase_grant"
	EntryPurchaseReversal = "purchase_reversal"
	EntryPlayDebit        = "play_debit"
	EntryPlayRefund       = "play_refund"
	EntryFree             = "free_entry"
	EntryAdminGrant       = "admin_grant"
	EntryAdminDeduction   = "admin_deduction"
)

// Why the gate said no.
const (
	GateAccount       = "ACCOUNT_NOT_ELIGIBLE"
	GateProgramOff    = "GAMES_NOT_AVAILABLE"
	GateStopped       = "GAMES_PAUSED"
	GateStateRequired = "STATE_REQUIRED"
	GateRegionOff     = "REGION_NOT_AVAILABLE"
	GateAgeRequired   = "AGE_REQUIRED"
	GateUnderAge      = "UNDER_AGE"
	GateGameOff       = "GAME_NOT_AVAILABLE"
)

// EntryGate is whether a customer may earn or spend entries, and the rules
// of where they are.
type EntryGate struct {
	Allowed           bool       `json:"allowed"`
	Code              string     `json:"code,omitempty"`
	Message           string     `json:"message,omitempty"`
	CountryID         uuid.UUID  `json:"country_id"`
	StateID           *uuid.UUID `json:"state_id"`
	JurisdictionID    *uuid.UUID `json:"-"`
	MinAge            int        `json:"min_age,omitempty"`
	OfficialRules     *string    `json:"official_rules,omitempty"`
	CustomerDailyCap  *int       `json:"customer_daily_cap,omitempty"`
	ProductDailyCap   *int       `json:"product_daily_cap,omitempty"`
	FreeEntriesPerDay int        `json:"free_entries_per_day"`
	Timezone          string     `json:"-"`
}

// GameEntryRow is one line of a customer's entries history.
type GameEntryRow struct {
	ID               uuid.UUID  `json:"id"`
	EntryType        string     `json:"entry_type"`
	Delta            int64      `json:"delta"`
	BalanceAfter     int64      `json:"balance_after"`
	Description      string     `json:"description"`
	OrderID          *uuid.UUID `json:"order_id,omitempty"`
	OrderNumber      *string    `json:"order_number,omitempty"`
	ProductName      *string    `json:"product_name,omitempty"`
	CompetitionID    *uuid.UUID `json:"competition_id,omitempty"`
	CompetitionTitle *string    `json:"competition_title,omitempty"`
	Reason           *string    `json:"reason,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

// GameEntriesTotals sums a customer's history. Pending is promised on orders
// whose payment has not been confirmed yet; it cannot be spent.
type GameEntriesTotals struct {
	Earned   int64 `json:"earned"`
	Spent    int64 `json:"spent"`
	Reversed int64 `json:"reversed"`
	Pending  int64 `json:"pending"`
}

// GameEntriesWallet is a customer's entries.
type GameEntriesWallet struct {
	Balance int64             `json:"balance"`
	Totals  GameEntriesTotals `json:"totals"`
	History []GameEntryRow    `json:"history"`
	// Gate is filled in for the customer's own view: whether they can earn
	// and play where they are.
	Earn *EntryGate `json:"earn,omitempty"`
	Play *EntryGate `json:"play,omitempty"`
}

// EntryShortfall is entries that could not be taken back after a refund
// because they had been used.
type EntryShortfall struct {
	ID               uuid.UUID  `json:"id"`
	CustomerID       uuid.UUID  `json:"customer_id"`
	CustomerEmail    string     `json:"customer_email"`
	CustomerName     *string    `json:"customer_name"`
	OrderID          uuid.UUID  `json:"order_id"`
	OrderNumber      *string    `json:"order_number"`
	EntriesGranted   int        `json:"entries_granted"`
	EntriesRecovered int        `json:"entries_recovered"`
	EntriesShort     int        `json:"entries_short"`
	Status           string     `json:"status"`
	Resolution       *string    `json:"resolution"`
	ResolvedAt       *time.Time `json:"resolved_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

// LegacyBalance is one holder of retired points.
type LegacyBalance struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Email  string    `json:"email"`
	Points int64     `json:"points"`
}

// LegacyPoints is what is still held in the retired points system.
type LegacyPoints struct {
	CustomerPoints   int64           `json:"customer_points"`
	CustomersHolding int             `json:"customers_holding"`
	SellerPoints     int64           `json:"seller_points"`
	SellersHolding   int             `json:"sellers_holding"`
	Customers        []LegacyBalance `json:"customers"`
	Sellers          []LegacyBalance `json:"sellers"`
}

// ─── States ───────────────────────────────────────────────────────────────

// State is a state or region of a country.
type State struct {
	ID        uuid.UUID `json:"id"`
	CountryID uuid.UUID `json:"country_id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"is_active"`
}

// ─── Where games are on ───────────────────────────────────────────────────

// EntryProgram is the global switch.
type EntryProgram struct {
	Enabled       bool       `json:"enabled"`
	StoppedAt     *time.Time `json:"stopped_at"`
	StoppedReason *string    `json:"stopped_reason"`
	// State is OFF, ON or STOPPED, for display.
	State string `json:"state"`
}

// Jurisdiction is the games and entries settings of a country or state.
type Jurisdiction struct {
	ID                 uuid.UUID  `json:"id"`
	CountryID          uuid.UUID  `json:"country_id"`
	CountryName        string     `json:"country_name"`
	StateID            *uuid.UUID `json:"state_id"`
	StateName          *string    `json:"state_name"`
	EarningEnabled     bool       `json:"earning_enabled"`
	PlayEnabled        bool       `json:"play_enabled"`
	ChanceGamesEnabled bool       `json:"chance_games_enabled"`
	MinAge             int        `json:"min_age"`
	OfficialRules      *string    `json:"official_rules"`
	CustomerDailyCap   *int       `json:"customer_daily_cap"`
	ProductDailyCap    *int       `json:"product_daily_cap"`
	FreeEntriesPerDay  int        `json:"free_entries_per_day"`
	StoppedAt          *time.Time `json:"stopped_at"`
	StoppedReason      *string    `json:"stopped_reason"`
	// GameIDs are the games switched on here.
	GameIDs   []uuid.UUID `json:"game_ids"`
	State     string      `json:"state"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// ─── Advertising packages ─────────────────────────────────────────────────

const (
	AdPackageDraft     = "draft"
	AdPackagePublished = "published"
	AdPackageRetired   = "retired"

	AdPurchasePending   = "pending"
	AdPurchaseActive    = "active"
	AdPurchaseExhausted = "exhausted"
	AdPurchaseExpired   = "expired"
	AdPurchaseCancelled = "cancelled"
	AdPurchaseFailed    = "failed"
	AdPurchaseRefunded  = "refunded"
)

// AdPackage is one version of an advertising package, for one country.
// LevelEntries[0] is what a customer earns for buying the product in the
// top priority level, and so on down.
type AdPackage struct {
	ID           uuid.UUID  `json:"id"`
	PackageKey   uuid.UUID  `json:"package_key"`
	Version      int        `json:"version"`
	CountryID    uuid.UUID  `json:"country_id"`
	CountryName  string     `json:"country_name,omitempty"`
	Name         string     `json:"name"`
	Description  *string    `json:"description"`
	Currency     string     `json:"currency"`
	PriceMinor   int64      `json:"price_minor"`
	ProductSlots int        `json:"product_slots"`
	LevelEntries []int32    `json:"level_entries"`
	EntriesPool  int        `json:"entries_pool"`
	ValidityDays int        `json:"validity_days"`
	Status       string     `json:"status"`
	PublishedAt  *time.Time `json:"published_at"`
	RetiredAt    *time.Time `json:"retired_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// Purchases is how many sellers have bought this version (admin view).
	Purchases int `json:"purchases"`
}

// AdPurchase is a seller's purchase of a package, on the terms it had then.
type AdPurchase struct {
	ID                uuid.UUID  `json:"id"`
	SellerID          uuid.UUID  `json:"seller_id"`
	SellerName        string     `json:"seller_name,omitempty"`
	SellerEmail       string     `json:"seller_email,omitempty"`
	PackageID         uuid.UUID  `json:"package_id"`
	PackageName       string     `json:"package_name"`
	CountryID         uuid.UUID  `json:"country_id"`
	Currency          string     `json:"currency"`
	PriceMinor        int64      `json:"price_minor"`
	ProductSlots      int        `json:"product_slots"`
	LevelEntries      []int32    `json:"level_entries"`
	EntriesPool       int        `json:"entries_pool"`
	ValidityDays      int        `json:"validity_days"`
	Status            string     `json:"status"`
	EntriesUsed       int        `json:"entries_used"`
	EntriesHeld       int        `json:"entries_held"`
	Provider          string     `json:"provider"`
	ProviderReference *string    `json:"provider_reference,omitempty"`
	CheckoutURL       *string    `json:"checkout_url,omitempty"`
	ReceiptURL        *string    `json:"receipt_url,omitempty"`
	PaidAmountMinor   *int64     `json:"paid_amount_minor,omitempty"`
	FailureReason     *string    `json:"failure_reason,omitempty"`
	LastReminder      int        `json:"last_reminder_threshold"`
	PaidAt            *time.Time `json:"paid_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
	EndedAt           *time.Time `json:"ended_at"`
	CreatedAt         time.Time  `json:"created_at"`
	// UsagePercent is how much of the budget customers have been granted.
	UsagePercent int `json:"usage_percent"`
	// Promotions are the products on this package's list, by level.
	Promotions []AdPromotion `json:"promotions,omitempty"`
}

// AdPromotion is a product in one priority level of a purchase.
type AdPromotion struct {
	ID                 uuid.UUID  `json:"id"`
	PurchaseID         uuid.UUID  `json:"purchase_id"`
	ProductID          uuid.UUID  `json:"product_id"`
	ProductName        string     `json:"product_name"`
	ProductImageURL    *string    `json:"product_image_url"`
	Level              int        `json:"level"`
	EntriesPerPurchase int        `json:"entries_per_purchase"`
	EntriesGranted     int        `json:"entries_granted"`
	OrdersCount        int        `json:"orders_count"`
	RemovedAt          *time.Time `json:"removed_at"`
}

// AdReminder is a budget notice to a seller.
type AdReminder struct {
	ID           uuid.UUID  `json:"id"`
	PurchaseID   uuid.UUID  `json:"purchase_id"`
	PackageName  string     `json:"package_name"`
	Threshold    int        `json:"threshold"`
	UsagePercent int        `json:"usage_percent"`
	Kind         string     `json:"kind"`
	Message      string     `json:"message"`
	ReadAt       *time.Time `json:"read_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

// AdReminderEmail is a reminder waiting to be emailed.
type AdReminderEmail struct {
	AdReminder
	SellerEmail string
	SellerName  string
}

// SellerAdvertising is the seller's advertising dashboard.
type SellerAdvertising struct {
	Purchases []AdPurchase `json:"purchases"`
	Reminders []AdReminder `json:"reminders"`
	// CanBuy is false while the seller's country has no published package.
	Packages []AdPackage `json:"packages"`
}

// CustomerEntriesSummary is one customer in the admin customer search.
type CustomerEntriesSummary struct {
	CustomerID  uuid.UUID `json:"customer_id"`
	Email       string    `json:"email"`
	DisplayName *string   `json:"display_name,omitempty"`
	CountryName string    `json:"country_name"`
	StateName   *string   `json:"state_name,omitempty"`
	Status      string    `json:"status"`
	Balance     int64     `json:"balance"`
	CreatedAt   time.Time `json:"created_at"`
}
