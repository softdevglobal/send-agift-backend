package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Prize ledger entry types (spec §3.3).
const (
	PrizeEntrySeed             = "seed"
	PrizeEntryPlayIncrement    = "play_increment"
	PrizeEntryAdminAdjustment  = "admin_adjustment"
	PrizeEntryPlayReversal     = "play_reversal"
	PrizeEntryWinnerSettlement = "winner_settlement"
	PrizeEntryCorrection       = "correction"
)

// PrizeLedgerEntry maps to competition.prize_ledger. Rows are never changed:
// corrections are new rows.
type PrizeLedgerEntry struct {
	ID                uuid.UUID  `json:"id"`
	Seq               int64      `json:"seq"`
	CompetitionID     uuid.UUID  `json:"competition_id"`
	AttemptID         *uuid.UUID `json:"attempt_id,omitempty"`
	WinnerID          *uuid.UUID `json:"winner_id,omitempty"`
	EntryType         string     `json:"entry_type"`
	AmountDeltaCents  int64      `json:"amount_delta_cents"`
	BalanceAfterCents int64      `json:"balance_after_cents"`
	Reason            *string    `json:"reason,omitempty"`
	ActorType         string     `json:"actor_type"`
	ActorID           *uuid.UUID `json:"actor_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

// ReconciliationCheck is one invariant the reconciliation job verified.
type ReconciliationCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Expected int64  `json:"expected"`
	Actual   int64  `json:"actual"`
	Detail   string `json:"detail,omitempty"`
}

// Reconciliation recomputes a round's prize from its ledger and checks it
// against the cached prize, the plays and the points charged (AC-12).
type Reconciliation struct {
	CompetitionID     uuid.UUID             `json:"competition_id"`
	Status            string                `json:"status"` // ok or discrepancy
	LedgerTotalCents  int64                 `json:"ledger_total_cents"`
	CurrentPrizeCents int64                 `json:"current_prize_cents"`
	Checks            []ReconciliationCheck `json:"checks"`
	CheckedAt         time.Time             `json:"checked_at"`
}

// PrizeLedgerView is the admin ledger page: every entry, oldest first, and a
// fresh reconciliation.
type PrizeLedgerView struct {
	Entries        []PrizeLedgerEntry `json:"entries"`
	Reconciliation Reconciliation     `json:"reconciliation"`
}

// AdminPlay is one play as the admin ledger and void tools see it.
type AdminPlay struct {
	ID                  uuid.UUID  `json:"id"`
	CustomerID          uuid.UUID  `json:"customer_id"`
	DisplayName         *string    `json:"display_name,omitempty"`
	AttemptNumber       int        `json:"attempt_number"`
	Status              string     `json:"status"`
	PointsSpent         int        `json:"points_spent"`
	PrizeIncrementCents int64      `json:"prize_increment_cents"`
	PrizeAfterCents     *int64     `json:"prize_after_cents,omitempty"`
	Score               *int64     `json:"score,omitempty"`
	VoidReason          *string    `json:"void_reason,omitempty"`
	RefundedAt          *time.Time `json:"refunded_at,omitempty"`
	StartedAt           time.Time  `json:"started_at"`
	// A chance play's outcome and the draw behind it.
	Result json.RawMessage `json:"result,omitempty"`
}

// PlayerActivity is one customer's play volume, for the fraud panel.
type PlayerActivity struct {
	CustomerID  uuid.UUID `json:"customer_id"`
	DisplayName *string   `json:"display_name,omitempty"`
	Plays       int64     `json:"plays"`
	LastHour    int64     `json:"last_hour"`
}

// SharedSource is a device or network more than one account played from.
type SharedSource struct {
	Source   string `json:"source"`
	Accounts int64  `json:"accounts"`
	Plays    int64  `json:"plays"`
}

// CompetitionAnalytics is the Super Admin dashboard for one round (spec §10).
type CompetitionAnalytics struct {
	CompetitionID           uuid.UUID        `json:"competition_id"`
	Currency                *string          `json:"currency,omitempty"`
	CurrentPrizeCents       int64            `json:"current_prize_cents"`
	StartPrizeCents         int64            `json:"start_prize_cents"`
	PrizeGrowthCents        int64            `json:"prize_growth_cents"`
	MaxPrizeCents           *int64           `json:"max_prize_cents,omitempty"`
	MaxPossibleLiability    *int64           `json:"max_possible_liability_cents,omitempty"`
	ValidPlays              int64            `json:"valid_plays"`
	VoidedPlays             int64            `json:"voided_plays"`
	UniquePlayers           int64            `json:"unique_players"`
	RepeatPlayers           int64            `json:"repeat_players"`
	RepeatPlayRate          float64          `json:"repeat_play_rate"`
	PointsSpent             int64            `json:"points_spent"`
	PointsRefunded          int64            `json:"points_refunded"`
	NetPointsConsumed       int64            `json:"net_points_consumed"`
	IncrementsCents         int64            `json:"increments_cents"`
	AdjustmentsCents        int64            `json:"adjustments_cents"`
	ReversalsCents          int64            `json:"reversals_cents"`
	CorrectionsCents        int64            `json:"corrections_cents"`
	SettledCents            int64            `json:"settled_cents"`
	RejectedByReason        map[string]int64 `json:"rejected_by_reason"`
	UniqueViewers           int64            `json:"unique_viewers"`
	ViewToPlayRate          float64          `json:"view_to_play_rate"`
	TopPlayers              []PlayerActivity `json:"top_players"`
	VelocityAlerts          []PlayerActivity `json:"velocity_alerts"`
	// Several accounts playing from one device or one network (spec §10).
	SharedDevices  []SharedSource `json:"shared_devices"`
	SharedNetworks []SharedSource `json:"shared_networks"`
	ReconciliationStatus    string           `json:"reconciliation_status"`
	ReconciliationCheckedAt *time.Time       `json:"reconciliation_checked_at,omitempty"`
}

// PrizeEvent is one live update for a round (spec §5.4), read from the
// outbox after its transaction committed.
type PrizeEvent struct {
	Seq     int64           `json:"-"`
	Type    string          `json:"event"`
	Payload json.RawMessage `json:"-"`
}
