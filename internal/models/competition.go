package models

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/games"
)

// CompetitionCountry is one country a competition runs in.
type CompetitionCountry struct {
	ID              uuid.UUID `json:"id"`
	IsoCode         string    `json:"iso_code"`
	Name            string    `json:"name"`
	DefaultCurrency string    `json:"default_currency"`
}

// Competition maps to competition.competitions, joined with the countries it
// runs in and the game version it locks.
type Competition struct {
	ID                uuid.UUID            `json:"id"`
	Countries         []CompetitionCountry `json:"countries"`
	GameVersionID     uuid.UUID            `json:"game_version_id"`
	GameVersionStatus string               `json:"game_version_status"`
	GameSlug          string               `json:"game_slug"`
	GameName          string               `json:"game_name"`
	GameType          string               `json:"game_type"`
	GameVersion       string               `json:"game_version"`
	GameConfig        json.RawMessage      `json:"-"`
	Title             string               `json:"title"`
	Status            string               `json:"status"`
	StartsAt          time.Time            `json:"starts_at"`
	EndsAt            time.Time            `json:"ends_at"`
	Timezone          string               `json:"timezone"`
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

	// Progressive prize economics (Progressive Prize spec §2). All money is
	// minor units of PrizeCurrency.
	PrizeGrowthEnabled bool   `json:"prize_growth_enabled"`
	PrizeType          string `json:"prize_type"`
	WinnerMethod       string `json:"winner_method"`
	// Instant-win rounds: each play wins with probability 1 in WinOdds.
	WinOdds *int `json:"win_odds,omitempty"`
	// Points prizes: what each validated winner receives.
	PrizePoints *int64 `json:"prize_points,omitempty"`
	// Quiz rounds: the questions with their answers. Loaded only for admins;
	// players get them without answers, one play at a time.
	QuizQuestions         []games.QuizQuestion `json:"quiz_questions,omitempty"`
	StartPrizeCents       int64                `json:"start_prize_cents"`
	IncrementPerPlayCents int64                `json:"increment_per_play_cents"`
	MaxPrizeCents         *int64               `json:"max_prize_cents,omitempty"`
	ContinueAtCap         bool                 `json:"continue_at_cap"`
	DailyPlayLimit        *int                 `json:"daily_play_limit,omitempty"`
	MinPlaysToWin         *int                 `json:"min_plays_to_win,omitempty"`
	CurrentPrizeCents     int64                `json:"current_prize_cents"`
	EligiblePlayCount     int64                `json:"eligible_play_count"`
	UniquePlayerCount     int64                `json:"unique_player_count"`
	PrizeVersion          int64                `json:"prize_version"`
	FinalPrizeCents       *int64               `json:"final_prize_cents,omitempty"`
	RoundNo               int                  `json:"round_no"`
	PreviousRoundID       *uuid.UUID           `json:"previous_round_id,omitempty"`
	ConfigVersion         int                  `json:"config_version"`
	PausedAt              *time.Time           `json:"paused_at,omitempty"`
	ClosedAt              *time.Time           `json:"closed_at,omitempty"`
	UpdatedByAdminID      *uuid.UUID           `json:"updated_by_admin_id,omitempty"`
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

	ClientRequestID     *string    `json:"-"`
	PrizeIncrementCents int64      `json:"prize_increment_cents"`
	PrizeBeforeCents    *int64     `json:"prize_before_cents,omitempty"`
	PrizeAfterCents     *int64     `json:"prize_after_cents,omitempty"`
	RefundedAt          *time.Time `json:"refunded_at,omitempty"`
	// A chance play's outcome and the draw behind it.
	ResultPayload json.RawMessage `json:"result,omitempty"`
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
	ID                uuid.UUID  `json:"id"`
	CompetitionID     uuid.UUID  `json:"competition_id"`
	CustomerID        uuid.UUID  `json:"customer_id"`
	ScoreSubmissionID *uuid.UUID `json:"score_submission_id,omitempty"`
	// Set for a chance round's winner: the play that won.
	AttemptID     *uuid.UUID  `json:"attempt_id,omitempty"`
	PrizePosition int         `json:"prize_position"`
	Rank          int         `json:"rank"`
	Status        string      `json:"status"`
	StatusReason  *string     `json:"status_reason,omitempty"`
	ValidatedAt   *time.Time  `json:"validated_at,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	DisplayName   *string     `json:"display_name,omitempty"`
	CountryName   string      `json:"country_name"`
	Score         int64       `json:"score"`
	DurationMs    int64       `json:"duration_ms"`
	AchievedAt    time.Time   `json:"achieved_at"`
	Claim         *PrizeClaim `json:"claim,omitempty"`

	PrizeValueCents     *int64     `json:"prize_value_cents,omitempty"`
	SettlementStatus    string     `json:"settlement_status"`
	SettlementReference *string    `json:"settlement_reference,omitempty"`
	SettledAt           *time.Time `json:"settled_at,omitempty"`
}

// HasCountry reports whether players from the country may enter.
func (c *Competition) HasCountry(id uuid.UUID) bool {
	for _, co := range c.Countries {
		if co.ID == id {
			return true
		}
	}
	return false
}

// CountryIDs lists the countries the competition runs in.
func (c *Competition) CountryIDs() []uuid.UUID {
	out := make([]uuid.UUID, len(c.Countries))
	for i, co := range c.Countries {
		out[i] = co.ID
	}
	return out
}

// CountryNames joins the country names with ", ".
func (c *Competition) CountryNames() string {
	names := make([]string, len(c.Countries))
	for i, co := range c.Countries {
		names[i] = co.Name
	}
	return strings.Join(names, ", ")
}

// CountryCodes joins the country ISO codes with ", ".
func (c *Competition) CountryCodes() string {
	codes := make([]string, len(c.Countries))
	for i, co := range c.Countries {
		codes[i] = co.IsoCode
	}
	return strings.Join(codes, ", ")
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
	ID           uuid.UUID            `json:"id"`
	Title        string               `json:"title"`
	Status       string               `json:"status"`
	GameSlug     string               `json:"game_slug"`
	GameName     string               `json:"game_name"`
	GameType     string               `json:"game_type"`
	WinnerMethod string               `json:"winner_method"`
	WinOdds      *int                 `json:"win_odds,omitempty"`
	Countries    []CompetitionCountry `json:"countries"`
	// The countries' codes and names joined with ", ", for screens that
	// show them on one line.
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

	// The live prize (spec §6.1). For a fixed prize, current equals start
	// and nothing grows.
	PrizeGrowthEnabled    bool   `json:"prize_growth_enabled"`
	PrizeType             string `json:"prize_type"`
	PrizePoints           *int64 `json:"prize_points,omitempty"`
	StartPrizeCents       int64  `json:"start_prize_cents"`
	CurrentPrizeCents     int64  `json:"current_prize_cents"`
	IncrementPerPlayCents int64  `json:"increment_per_play_cents"`
	MaxPrizeCents         *int64 `json:"max_prize_cents,omitempty"`
	// True once the prize has reached its cap: plays add nothing more.
	PrizeCapReached   bool   `json:"prize_cap_reached"`
	ContinueAtCap     bool   `json:"continue_at_cap"`
	FinalPrizeCents   *int64 `json:"final_prize_cents,omitempty"`
	EligiblePlayCount int64  `json:"eligible_play_count"`
	UniquePlayerCount int64  `json:"unique_player_count"`
	DailyPlayLimit    *int   `json:"daily_play_limit,omitempty"`
	PrizeVersion      int64  `json:"prize_version"`
	RoundNo           int    `json:"round_no"`
}

// CompetitionMe is the signed-in customer's position in one competition.
type CompetitionMe struct {
	AttemptsUsed      int `json:"attempts_used"`
	AttemptsRemaining int `json:"attempts_remaining"`
	// Plays left today under the daily limit, when the round has one, and
	// when the next day's plays open.
	PlaysLeftToday   *int       `json:"plays_left_today,omitempty"`
	DailyResetAt     *time.Time `json:"daily_reset_at,omitempty"`
	PointsBalance    int64      `json:"points_balance"`
	BestScore        *int64     `json:"best_score,omitempty"`
	Rank             *int       `json:"rank,omitempty"`
	Eligible         bool       `json:"eligible"`
	IneligibleReason string     `json:"ineligible_reason,omitempty"`
	Win              *MyWin     `json:"win,omitempty"`
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

	// The play receipt (spec §5.2). A retried request with the same
	// idempotency key returns the original receipt with Replayed set.
	PlayID                uuid.UUID `json:"play_id"`
	Status                string    `json:"status"`
	WalletPointsRemaining int64     `json:"wallet_points_remaining"`
	PrizeBeforeCents      int64     `json:"prize_before_cents"`
	PrizeIncrementCents   int64     `json:"prize_increment_cents"`
	PrizeAfterCents       int64     `json:"prize_after_cents"`
	PrizeCapReached       bool      `json:"prize_cap_reached"`
	PlayedAt              time.Time `json:"played_at"`
	Replayed              bool      `json:"replayed"`
	// The game-specific result (spec §5.2): a chance play's outcome. Skill
	// plays have none until their score is submitted.
	Result json.RawMessage `json:"result,omitempty"`
}

// AdminCompetitionView adds the operational details admins need.
type AdminCompetitionView struct {
	Competition
	EffectiveStatus string        `json:"effective_status"`
	Reserve         *PrizeReserve `json:"prize_reserve,omitempty"`
	Attempts        int           `json:"attempts"`
	Submissions     int           `json:"submissions"`
	UnderReview     int           `json:"under_review"`
	// Why the round cannot be scheduled yet, if anything; empty once it can.
	ScheduleBlockers []string `json:"schedule_blockers"`
	// The push announcement sent when it was published.
	Announcement AnnouncementStats `json:"announcement"`
}

// AnnouncementStats counts a competition's push announcement by outcome.
type AnnouncementStats struct {
	Queued  int `json:"queued"`
	Pending int `json:"pending"`
	Sent    int `json:"sent"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}
