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
)

// PointsEntry maps to finance.points_ledger.
type PointsEntry struct {
	ID            uuid.UUID  `json:"id"`
	EntryType     string     `json:"entry_type"`
	AmountDelta   int64      `json:"amount_delta"`
	BalanceAfter  int64      `json:"balance_after"`
	CompetitionID *uuid.UUID `json:"competition_id,omitempty"`
	AttemptID     *uuid.UUID `json:"attempt_id,omitempty"`
	OrderID       *uuid.UUID `json:"order_id,omitempty"`
	Reason        *string    `json:"reason,omitempty"`
	ActorType     string     `json:"actor_type"`
	CreatedAt     time.Time  `json:"created_at"`
	// Joined for display.
	CompetitionTitle *string `json:"competition_title,omitempty"`
}

// PointsWallet is a customer's balance and recent history.
type PointsWallet struct {
	CustomerID     uuid.UUID     `json:"customer_id"`
	Balance        int64         `json:"balance"`
	LifetimeEarned int64         `json:"lifetime_earned"`
	LifetimeSpent  int64         `json:"lifetime_spent"`
	Entries        []PointsEntry `json:"entries"`
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
}
