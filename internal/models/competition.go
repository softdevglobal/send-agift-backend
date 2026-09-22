package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Competition maps to competition.competitions, joined with its country and
// the game version it locks.
type Competition struct {
	ID                uuid.UUID       `json:"id"`
	CountryID         uuid.UUID       `json:"country_id"`
	CountryCode       string          `json:"country_code"`
	CountryName       string          `json:"country_name"`
	GameVersionID     uuid.UUID       `json:"game_version_id"`
	GameVersionStatus string          `json:"game_version_status"`
	GameSlug          string          `json:"game_slug"`
	GameName          string          `json:"game_name"`
	GameVersion       string          `json:"game_version"`
	GameConfig        json.RawMessage `json:"-"`
	Title             string          `json:"title"`
	Status            string          `json:"status"`
	StartsAt          time.Time       `json:"starts_at"`
	EndsAt            time.Time       `json:"ends_at"`
	Timezone          string          `json:"timezone"`
	// Never serialised: it is only handed out with a live attempt, so nobody
	// can study the board before the competition opens.
	ServerSeed                   string     `json:"-"`
	PointsPerAttempt             int        `json:"points_per_attempt"`
	MaxAttemptsPerCustomer       int        `json:"max_attempts_per_customer"`
	MinAge                       int        `json:"min_age"`
	RequiresIdentityVerification bool       `json:"requires_identity_verification"`
	NumberOfWinners              int        `json:"number_of_winners"`
	PrizeDescription             string     `json:"prize_description"`
	PrizeValueAmount             *int64     `json:"prize_value_amount,omitempty"`
	PrizeCurrency                *string    `json:"prize_currency,omitempty"`
	OfficialRules                *string    `json:"official_rules,omitempty"`
	OfficialRulesMediaID         *uuid.UUID `json:"official_rules_media_id,omitempty"`
	CancelReason                 *string    `json:"cancel_reason,omitempty"`
	CancelNote                   *string    `json:"cancel_note,omitempty"`
	CancelledAt                  *time.Time `json:"cancelled_at,omitempty"`
	FrozenAt                     *time.Time `json:"frozen_at,omitempty"`
	FinalisedAt                  *time.Time `json:"finalised_at,omitempty"`
	CreatedByAdminID             *uuid.UUID `json:"created_by_admin_id,omitempty"`
	CreatedAt                    time.Time  `json:"created_at"`
	UpdatedAt                    time.Time  `json:"updated_at"`
}

// PrizeReserve maps to finance.prize_reserves.
type PrizeReserve struct {
	ID                uuid.UUID  `json:"id"`
	CompetitionID     uuid.UUID  `json:"competition_id"`
	ReserveAmount     int64      `json:"reserve_amount"`
	Currency          string     `json:"currency"`
	FundingSource     string     `json:"funding_source"`
	Status            string     `json:"status"`
	EvidenceReference *string    `json:"evidence_reference,omitempty"`
	FundedAt          *time.Time `json:"funded_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// CompetitionAttempt maps to competition.competition_attempts.
type CompetitionAttempt struct {
	ID             uuid.UUID  `json:"id"`
	CompetitionID  uuid.UUID  `json:"competition_id"`
	CustomerID     uuid.UUID  `json:"-"`
	SessionID      uuid.UUID  `json:"session_id"`
	PointsLedgerID *uuid.UUID `json:"-"`
	PointsSpent    int        `json:"points_spent"`
	AttemptNumber  int        `json:"attempt_number"`
	Status         string     `json:"status"`
	VoidReason     *string    `json:"void_reason,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	SubmittedAt    *time.Time `json:"submitted_at,omitempty"`
}

// ScoreSubmission maps to competition.score_submissions.
type ScoreSubmission struct {
	ID                uuid.UUID        `json:"id"`
	CompetitionID     uuid.UUID        `json:"competition_id"`
	AttemptID         uuid.UUID        `json:"attempt_id"`
	CustomerID        uuid.UUID        `json:"customer_id"`
	Score             int64            `json:"score"`
	ClientScore       *int64           `json:"client_score,omitempty"`
	DurationMs        int64            `json:"duration_ms"`
	MovesCount        int              `json:"moves_count"`
	Stats             map[string]int64 `json:"stats"`
	EventLogHash      string           `json:"event_log_hash"`
	ValidationStatus  string           `json:"validation_status"`
	ReviewReason      *string          `json:"review_reason,omitempty"`
	ReviewedByAdminID *uuid.UUID       `json:"reviewed_by_admin_id,omitempty"`
	ReviewedAt        *time.Time       `json:"reviewed_at,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	// Joined for the admin review queue.
	DisplayName *string `json:"display_name,omitempty"`
}

// PrizeClaim maps to competition.prize_claims.
type PrizeClaim struct {
	ID                uuid.UUID  `json:"id"`
	WinnerID          uuid.UUID  `json:"winner_id"`
	CustomerID        uuid.UUID  `json:"-"`
	ClaimDeadlineAt   time.Time  `json:"claim_deadline_at"`
	ClaimedAt         *time.Time `json:"claimed_at,omitempty"`
	Status            string     `json:"status"`
	DeliveryAddressID *uuid.UUID `json:"delivery_address_id,omitempty"`
	TermsAcceptedAt   *time.Time `json:"terms_accepted_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// CompetitionWinner maps to competition.competition_winners, joined with the
// winning score and the player's public details.
type CompetitionWinner struct {
	ID                uuid.UUID   `json:"id"`
	CompetitionID     uuid.UUID   `json:"competition_id"`
	CustomerID        uuid.UUID   `json:"customer_id"`
	ScoreSubmissionID uuid.UUID   `json:"score_submission_id"`
	PrizePosition     int         `json:"prize_position"`
	Rank              int         `json:"rank"`
	Status            string      `json:"status"`
	StatusReason      *string     `json:"status_reason,omitempty"`
	ValidatedAt       *time.Time  `json:"validated_at,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
	DisplayName       *string     `json:"display_name,omitempty"`
	CountryName       string      `json:"country_name"`
	Score             int64       `json:"score"`
	DurationMs        int64       `json:"duration_ms"`
	AchievedAt        time.Time   `json:"achieved_at"`
	Claim             *PrizeClaim `json:"claim,omitempty"`
}

// AuditEntry is one row for admin.audit_log.
type AuditEntry struct {
	ActorType  string
	ActorID    *uuid.UUID
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	Before     any
	After      any
	Reason     *string
	IPAddress  *string
	UserAgent  *string
}

// ─── Views ────────────────────────────────────────────────────────────────

// CompetitionView is what customers see. It carries the disclosures the rules
// require (§13.10) and never the seed.
type CompetitionView struct {
	ID                           uuid.UUID      `json:"id"`
	Title                        string         `json:"title"`
	Status                       string         `json:"status"`
	GameSlug                     string         `json:"game_slug"`
	GameName                     string         `json:"game_name"`
	CountryCode                  string         `json:"country_code"`
	CountryName                  string         `json:"country_name"`
	StartsAt                     time.Time      `json:"starts_at"`
	EndsAt                       time.Time      `json:"ends_at"`
	Timezone                     string         `json:"timezone"`
	PointsPerAttempt             int            `json:"points_per_attempt"`
	PointsDeductionEnabled       bool           `json:"points_deduction_enabled"`
	MaxAttemptsPerCustomer       int            `json:"max_attempts_per_customer"`
	MinAge                       int            `json:"min_age"`
	RequiresIdentityVerification bool           `json:"requires_identity_verification"`
	NumberOfWinners              int            `json:"number_of_winners"`
	PrizeDescription             string         `json:"prize_description"`
	PrizeValueAmount             *int64         `json:"prize_value_amount,omitempty"`
	PrizeCurrency                *string        `json:"prize_currency,omitempty"`
	OfficialRules                *string        `json:"official_rules,omitempty"`
	CancelReason                 *string        `json:"cancel_reason,omitempty"`
	CancelNote                   *string        `json:"cancel_note,omitempty"`
	Me                           *CompetitionMe `json:"me,omitempty"`
	Winners                      []PublicWinner `json:"winners,omitempty"`
}

// CompetitionMe is the signed-in customer's position in one competition.
type CompetitionMe struct {
	AttemptsUsed      int    `json:"attempts_used"`
	AttemptsRemaining int    `json:"attempts_remaining"`
	BestScore         *int64 `json:"best_score,omitempty"`
	Rank              *int   `json:"rank,omitempty"`
	Eligible          bool   `json:"eligible"`
	IneligibleReason  string `json:"ineligible_reason,omitempty"`
	Win               *MyWin `json:"win,omitempty"`
}

// MyWin is shown only to the winner themselves.
type MyWin struct {
	WinnerID        uuid.UUID  `json:"winner_id"`
	PrizePosition   int        `json:"prize_position"`
	Status          string     `json:"status"`
	ClaimID         *uuid.UUID `json:"claim_id,omitempty"`
	ClaimStatus     *string    `json:"claim_status,omitempty"`
	ClaimDeadlineAt *time.Time `json:"claim_deadline_at,omitempty"`
}

// PublicWinner is what may be published about a winner (§19.1): first name,
// surname initial, region, score and when it was achieved.
type PublicWinner struct {
	PrizePosition int       `json:"prize_position"`
	Rank          int       `json:"rank"`
	DisplayName   string    `json:"display_name"`
	CountryName   string    `json:"country_name"`
	Score         int64     `json:"score"`
	AchievedAt    time.Time `json:"achieved_at"`
}

// CompetitionLeaderboardView is one competition's live board.
type CompetitionLeaderboardView struct {
	CompetitionID uuid.UUID          `json:"competition_id"`
	Status        string             `json:"status"`
	Final         bool               `json:"final"`
	Entries       []LeaderboardEntry `json:"entries"`
	Me            *LeaderboardEntry  `json:"me,omitempty"`
	TotalPlayers  int                `json:"total_players"`
}

// AttemptStartView is returned when an official attempt begins. The session
// carries the competition's shared seed.
type AttemptStartView struct {
	AttemptID         uuid.UUID       `json:"attempt_id"`
	AttemptNumber     int             `json:"attempt_number"`
	AttemptsRemaining int             `json:"attempts_remaining"`
	PointsSpent       int             `json:"points_spent"`
	Session           GameSessionView `json:"session"`
}

// AdminCompetitionView adds the operational details admins need.
type AdminCompetitionView struct {
	Competition
	EffectiveStatus string        `json:"effective_status"`
	Reserve         *PrizeReserve `json:"prize_reserve,omitempty"`
	Attempts        int           `json:"attempts"`
	Submissions     int           `json:"submissions"`
	UnderReview     int           `json:"under_review"`
}
