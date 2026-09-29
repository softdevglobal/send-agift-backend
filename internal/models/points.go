package models

import (
	"time"

	"github.com/google/uuid"
)

// Points ledger entry types.
const (
	PointsEntryAdminGrant     = "admin_grant"
	PointsEntryAdminDeduction = "admin_deduction"
	PointsEntryPlayDebit      = "play_debit"
	PointsEntryPlayRefund     = "play_refund"
	PointsEntryCorrection     = "correction"
	PointsEntryOrderReward    = "order_reward"
	PointsEntryOrderReversal  = "order_reversal"
	PointsEntrySignupBonus    = "signup_bonus"

	// A product's own reward points, paid when the line is delivered, and
	// taken back if the order is refunded.
	PointsEntryProductReward         = "product_reward"
	PointsEntryProductRewardReversal = "product_reward_reversal"
	// Points a customer attached to a gift: out of the sender's balance at
	// checkout, into the recipient's on delivery, back to the sender when it
	// never arrives.
	PointsEntryGiftSent     = "gift_points_sent"
	PointsEntryGiftReceived = "gift_points_received"
	PointsEntryGiftReturned = "gift_points_returned"
	PointsEntryGiftReversal = "gift_points_reversal"
	// Points won as a competition prize.
	PointsEntryPrizePoints = "prize_points"

	// Sellers' entries: points bought, and points paid out as product rewards
	// (or returned when a rewarded order is refunded).
	PointsEntryPointsPurchase      = "points_purchase"
	PointsEntryRewardFunding       = "reward_funding"
	PointsEntryRewardFundingReturn = "reward_funding_return"
)

// Points transaction categories: the kinds of change a person reads in their
// history. Several ledger entry types can share one.
const (
	PointsCategoryProductPurchase = "PRODUCT_PURCHASE"
	PointsCategoryGiftReward      = "GIFT_REWARD"
	PointsCategoryGiftSent        = "GIFT_SENT"
	PointsCategoryGameEntry       = "GAME_ENTRY"
	PointsCategoryPointsPurchase  = "POINTS_PURCHASE"
	PointsCategoryRewardFunding   = "REWARD_FUNDING"
	PointsCategoryRefund          = "REFUND"
	PointsCategoryAdminAdjustment = "ADMIN_ADJUSTMENT"
)

// PointsCategory is the category an entry type belongs to.
func PointsCategory(entryType string) string {
	switch entryType {
	case PointsEntryOrderReward, PointsEntryProductReward:
		return PointsCategoryProductPurchase
	case PointsEntryGiftReceived, PointsEntryPrizePoints, PointsEntrySignupBonus:
		return PointsCategoryGiftReward
	case PointsEntryGiftSent:
		return PointsCategoryGiftSent
	case PointsEntryPlayDebit:
		return PointsCategoryGameEntry
	case PointsEntryPointsPurchase:
		return PointsCategoryPointsPurchase
	case PointsEntryRewardFunding:
		return PointsCategoryRewardFunding
	case PointsEntryPlayRefund, PointsEntryOrderReversal, PointsEntryProductRewardReversal,
		PointsEntryGiftReturned, PointsEntryGiftReversal, PointsEntryRewardFundingReturn:
		return PointsCategoryRefund
	default:
		return PointsCategoryAdminAdjustment
	}
}

// PointsEntry maps to finance.points_ledger. Exactly one of CustomerID and
// SellerID is set.
type PointsEntry struct {
	ID         uuid.UUID  `json:"id"`
	CustomerID *uuid.UUID `json:"customer_id,omitempty"`
	SellerID   *uuid.UUID `json:"seller_id,omitempty"`
	EntryType  string     `json:"entry_type"`
	// Category is the kind of change, e.g. PRODUCT_PURCHASE or GAME_ENTRY.
	Category string `json:"category"`
	// Direction is "credit" or "debit"; Amount is always positive, and
	// AmountDelta carries the sign.
	Direction     string     `json:"direction"`
	Amount        int64      `json:"amount"`
	AmountDelta   int64      `json:"amount_delta"`
	BalanceBefore int64      `json:"balance_before"`
	BalanceAfter  int64      `json:"balance_after"`
	Status        string     `json:"status"`
	CompetitionID *uuid.UUID `json:"competition_id,omitempty"`
	AttemptID     *uuid.UUID `json:"attempt_id,omitempty"`
	OrderID       *uuid.UUID `json:"order_id,omitempty"`
	ReferenceType *string    `json:"reference_type,omitempty"`
	ReferenceID   *uuid.UUID `json:"reference_id,omitempty"`
	Reason        *string    `json:"reason,omitempty"`
	// Description is a line a person can read, e.g. "Purchased Wireless
	// Headphones".
	Description string    `json:"description"`
	ActorType   string    `json:"actor_type"`
	CreatedAt   time.Time `json:"created_at"`
	// Joined for display.
	CompetitionTitle *string `json:"competition_title,omitempty"`
	OrderNumber      *string `json:"order_number,omitempty"`
	ProductName      *string `json:"product_name,omitempty"`
}

// PointsTotals is where a customer's points came from and went.
type PointsTotals struct {
	// Net of any taken back after a refund.
	FromPurchases int64 `json:"from_purchases"`
	FromGifts     int64 `json:"from_gifts"`
	FromPrizes    int64 `json:"from_prizes"`
	// Net of any plays refunded.
	SpentOnGames int64 `json:"spent_on_games"`
	// Net of any returned because the gift never arrived.
	SentAsGifts int64 `json:"sent_as_gifts"`
}

// PointsWallet is a customer's balance and recent history.
type PointsWallet struct {
	CustomerID     uuid.UUID     `json:"customer_id"`
	Balance        int64         `json:"balance"`
	LifetimeEarned int64         `json:"lifetime_earned"`
	LifetimeSpent  int64         `json:"lifetime_spent"`
	Totals         PointsTotals  `json:"totals"`
	Entries        []PointsEntry `json:"entries"`
}

// PointsRate is what a point costs a seller.
type PointsRate struct {
	CentsPerPoint int    `json:"cents_per_point"`
	Currency      string `json:"currency"`
}

// SellerPointsWallet is a seller's points: what they hold, what is promised
// to customers on undelivered orders, and their history.
type SellerPointsWallet struct {
	SellerID uuid.UUID `json:"seller_id"`
	Balance  int64     `json:"balance"`
	// Reserved is promised as rewards on orders not yet delivered.
	Reserved int64 `json:"reserved"`
	// Available is what can still be promised: Balance - Reserved.
	Available         int64         `json:"available"`
	LifetimePurchased int64         `json:"lifetime_purchased"`
	LifetimeSpent     int64         `json:"lifetime_spent"`
	Rate              PointsRate    `json:"rate"`
	PaymentProvider   string        `json:"payment_provider"`
	TestPayments      bool          `json:"test_payments"`
	Entries           []PointsEntry `json:"entries"`
}

// Points purchase statuses.
const (
	PointsPurchasePending   = "pending"
	PointsPurchaseCompleted = "completed"
	PointsPurchaseFailed    = "failed"
	PointsPurchaseCancelled = "cancelled"
)

// PointsPurchase maps to finance.points_purchases: a seller buying points.
type PointsPurchase struct {
	ID                uuid.UUID  `json:"id"`
	SellerID          uuid.UUID  `json:"seller_id"`
	AmountCents       int64      `json:"amount_cents"`
	Currency          string     `json:"currency"`
	CentsPerPoint     int        `json:"cents_per_point"`
	Points            int64      `json:"points"`
	PaidAmountCents   *int64     `json:"paid_amount_cents,omitempty"`
	PointsCredited    *int64     `json:"points_credited,omitempty"`
	Status            string     `json:"status"`
	Provider          string     `json:"provider"`
	ProviderReference *string    `json:"provider_reference,omitempty"`
	CheckoutURL       *string    `json:"checkout_url,omitempty"`
	FailureReason     *string    `json:"failure_reason,omitempty"`
	LedgerEntryID     *uuid.UUID `json:"ledger_entry_id,omitempty"`
	ConfirmedBy       *string    `json:"confirmed_by,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	FailedAt          *time.Time `json:"failed_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	// Joined for the admin list.
	SellerName  *string `json:"seller_name,omitempty"`
	SellerEmail *string `json:"seller_email,omitempty"`
}

// CustomerPointsSummary is one customer in the admin points search.
type CustomerPointsSummary struct {
	CustomerID  uuid.UUID `json:"customer_id"`
	Email       string    `json:"email"`
	DisplayName *string   `json:"display_name,omitempty"`
	CountryName string    `json:"country_name"`
	Status      string    `json:"status"`
	Balance     int64     `json:"balance"`
	CreatedAt   time.Time `json:"created_at"`
}

// PointsEarningRule is one country's earning rule.
type PointsEarningRule struct {
	CountryID        uuid.UUID  `json:"country_id"`
	CountryName      string     `json:"country_name"`
	Currency         string     `json:"currency"`
	Enabled          bool       `json:"enabled"`
	PointsPerUnit    int        `json:"points_per_unit"`
	EffectiveFrom    *time.Time `json:"effective_from,omitempty"`
	SignupBonus      int        `json:"signup_bonus"`
	SignupBonusSince *time.Time `json:"signup_bonus_since,omitempty"`
	// Whether the country's points_earning_enabled gate is on; the rule does
	// nothing without it.
	EarningAllowed bool       `json:"earning_allowed"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
}

// EarningRunResult is what one pass of the earning job did.
type EarningRunResult struct {
	OrdersRewarded  int   `json:"orders_rewarded"`
	PointsAwarded   int64 `json:"points_awarded"`
	OrdersReversed  int   `json:"orders_reversed"`
	PointsReversed  int64 `json:"points_reversed"`
	SignupBonuses   int   `json:"signup_bonuses"`
	BonusPointsPaid int64 `json:"bonus_points_paid"`

	ProductRewards      int   `json:"product_rewards"`
	ProductRewardPoints int64 `json:"product_reward_points"`
	RewardsReleased     int   `json:"rewards_released"`
	RewardsReversed     int   `json:"rewards_reversed"`
	GiftPointsDelivered int   `json:"gift_points_delivered"`
	GiftPointsReturned  int   `json:"gift_points_returned"`
	GiftPointsReversed  int   `json:"gift_points_reversed"`
}

// Anything reports whether the pass changed anything.
func (r EarningRunResult) Anything() bool {
	return r.OrdersRewarded+r.OrdersReversed+r.SignupBonuses+r.ProductRewards+r.RewardsReleased+
		r.RewardsReversed+r.GiftPointsDelivered+r.GiftPointsReturned+r.GiftPointsReversed > 0
}
