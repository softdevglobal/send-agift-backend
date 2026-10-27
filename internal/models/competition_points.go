package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Competition points (handover v4): a unit of its own, earned from
// qualifying gifts and a once-per-paid-period member bonus, spent on skill
// competitions. Legacy points (PointsWallet) are a different unit and are
// never converted into these.

// Version statuses shared by plan offers and reward policies.
const (
	VersionDraft            = "draft"
	VersionAwaitingApproval = "awaiting_approval"
	VersionPublished        = "published"
	VersionSuspended        = "suspended"
	VersionRetired          = "retired"
)

// Reason codes returned to clients, with a safe message and a next action.
const (
	ReasonLocationUnavailable       = "LOCATION_UNAVAILABLE"
	ReasonVerificationRequired      = "VERIFICATION_REQUIRED"
	ReasonChannelUnavailable        = "CHANNEL_UNAVAILABLE"
	ReasonOperationStopped          = "OPERATION_STOPPED"
	ReasonInsufficientEligible      = "INSUFFICIENT_ELIGIBLE_POINTS"
	ReasonBonusAlreadyAwarded       = "BONUS_ALREADY_AWARDED"
	ReasonQuoteExpired              = "QUOTE_EXPIRED"
	ReasonStartUncertain            = "START_UNCERTAIN"
	ReasonNoOffer                   = "NO_OFFER_AVAILABLE"
	ReasonAlreadyMember             = "ALREADY_MEMBER"
	ReasonProviderUnavailable       = "PROVIDER_UNAVAILABLE"
	ReasonEntryRequired             = "ENTRY_REQUIRED"
	ReasonCompetitionNotOpen        = "COMPETITION_NOT_OPEN"
	ReasonEntryLimitReached         = "ENTRY_LIMIT_REACHED"
	ReasonIdempotencyKeyConflict    = "IDEMPOTENCY_KEY_CONFLICT"
	ReasonCompetitionPointsDisabled = "COMPETITION_POINTS_NOT_USED"
)

// Operations an approval record can permit.
const (
	OpMembershipSale        = "membership_sale"
	OpMembershipRenewal     = "membership_renewal"
	OpGiftPointsEarning     = "gift_points_earning"
	OpCompetitionEntry      = "competition_entry"
	OpWinnerPayout          = "winner_payout"
	OpSellerCampaignFunding = "seller_campaign_funding"
)

// Emergency stop scopes.
const (
	StopMembershipSales     = "membership_sales"
	StopMembershipRenewals  = "membership_renewals"
	StopGiftPointsEarning   = "gift_points_earning"
	StopCompetitionEntries  = "competition_entries"
	StopCompetitionStarts   = "competition_starts"
	StopPayouts             = "payouts"
	StopCampaignCommitments = "campaign_commitments"
)

// Lot sources.
const (
	LotSourceGift         = "gift"
	LotSourceMemberBonus  = "member_bonus"
	LotSourceSponsored    = "sponsored"
	LotSourceCompensation = "compensation"
)

// When a membership's bonus is credited.
const (
	BonusTriggerPayment   = "payment"
	BonusTriggerFirstGift = "first_gift"
)

// PlanOffer maps to membership.plan_offers: one sellable version of a tier
// in one market.
type PlanOffer struct {
	ID                uuid.UUID `json:"id"`
	TierCode          string    `json:"tier_code"`
	Version           int       `json:"version"`
	CountryID         uuid.UUID `json:"country_id"`
	CountryName       string    `json:"country_name,omitempty"`
	Name              string    `json:"name"`
	Description       *string   `json:"description,omitempty"`
	Currency          string    `json:"currency"`
	PriceMinor        int64     `json:"price_minor"`
	IntervalUnit      string    `json:"interval_unit"`
	IntervalCount     int       `json:"interval_count"`
	MemberBonusPoints int64     `json:"member_bonus_points"`
	// MemberDiscountBPS is the percentage off the gift price a member gets,
	// in basis points (100 = 1%), funded from SendAGift's commission.
	MemberDiscountBPS int `json:"member_discount_bps"`
	// BonusTrigger is when the member bonus is credited: "payment" (once
	// the period is paid for) or "first_gift" (after the first qualifying
	// completed gift in the period).
	BonusTrigger       string          `json:"bonus_trigger"`
	GiftBenefits       json.RawMessage `json:"gift_benefits"`
	TermsVersion       string          `json:"terms_version"`
	Status             string          `json:"status"`
	EffectiveFrom      *time.Time      `json:"effective_from,omitempty"`
	CreatedByAdminID   *uuid.UUID      `json:"created_by_admin_id,omitempty"`
	SubmittedByAdminID *uuid.UUID      `json:"submitted_by_admin_id,omitempty"`
	ApprovedByAdminID  *uuid.UUID      `json:"approved_by_admin_id,omitempty"`
	PublishedAt        *time.Time      `json:"published_at,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	// BillingLabel is the true interval in words: "every 28 days", not
	// "monthly" for a 28-day plan.
	BillingLabel string `json:"billing_label"`
}

// MemberRow is one membership as the admin members list shows it: who, which
// package they are on, and where it stands.
type MemberRow struct {
	SubscriptionID uuid.UUID `json:"subscription_id"`
	CustomerID     uuid.UUID `json:"customer_id"`
	DisplayName    string    `json:"display_name"`
	Email          string    `json:"email"`
	CountryName    string    `json:"country_name"`
	OfferID        uuid.UUID `json:"offer_id"`
	PackageName    string    `json:"package_name"`
	TierCode       string    `json:"tier_code"`
	OfferVersion   int       `json:"offer_version"`
	PriceMinor     int64     `json:"price_minor"`
	Currency       string    `json:"currency"`
	BillingLabel   string    `json:"billing_label"`
	Status         string    `json:"status"`
	// CancelAtPeriodEnd: they stopped renewing and keep benefits to PeriodEnd.
	CancelAtPeriodEnd bool `json:"cancel_at_period_end"`
	// JoinedAt is their first paid period, falling back to when they started.
	JoinedAt time.Time `json:"joined_at"`
	// PeriodEnd is the end of the period paid for.
	PeriodEnd   *time.Time `json:"period_end,omitempty"`
	PeriodsPaid int        `json:"periods_paid"`
	TotalPaid   int64      `json:"total_paid_minor"`
}

// MemberPackageCount is how many current members are on one package.
type MemberPackageCount struct {
	TierCode    string `json:"tier_code"`
	PackageName string `json:"package_name"`
	CountryName string `json:"country_name"`
	Members     int    `json:"members"`
}

// Membership subscription statuses.
const (
	SubscriptionIncomplete = "incomplete"
	SubscriptionActive     = "active"
	SubscriptionPastDue    = "past_due"
	SubscriptionCancelling = "cancelling"
	SubscriptionCancelled  = "cancelled"
	SubscriptionExpired    = "expired"
	SubscriptionAbandoned  = "abandoned"
)

// Subscription maps to membership.subscriptions.
type Subscription struct {
	ID                     uuid.UUID  `json:"id"`
	CustomerID             uuid.UUID  `json:"customer_id"`
	OfferID                uuid.UUID  `json:"offer_id"`
	ScheduledOfferID       *uuid.UUID `json:"scheduled_offer_id,omitempty"`
	Provider               string     `json:"provider"`
	ProviderSubscriptionID *string    `json:"provider_subscription_id,omitempty"`
	CheckoutURL            *string    `json:"checkout_url,omitempty"`
	Status                 string     `json:"status"`
	CancelAtPeriodEnd      bool       `json:"cancel_at_period_end"`
	CurrentPeriodEnd       *time.Time `json:"current_period_end,omitempty"`
	CancelRequestedAt      *time.Time `json:"cancel_requested_at,omitempty"`
	EndedAt                *time.Time `json:"ended_at,omitempty"`
	RenewalConsentAt       time.Time  `json:"renewal_consent_at"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// PaidPeriod maps to membership.paid_periods.
type PaidPeriod struct {
	ID                uuid.UUID  `json:"id"`
	SubscriptionID    uuid.UUID  `json:"subscription_id"`
	OfferID           uuid.UUID  `json:"offer_id"`
	PeriodStart       time.Time  `json:"period_start"`
	PeriodEnd         time.Time  `json:"period_end"`
	AmountMinor       int64      `json:"amount_minor"`
	Currency          string     `json:"currency"`
	Status            string     `json:"status"`
	PaidAt            time.Time  `json:"paid_at"`
	StatusChangedAt   *time.Time `json:"status_changed_at,omitempty"`
	BonusSlot         *BonusSlot `json:"bonus_slot,omitempty"`
	ProviderInvoiceID string     `json:"-"`
}

// Bonus slot statuses.
const (
	BonusUnawarded = "unawarded"
	BonusAwarded   = "awarded"
	BonusReversed  = "reversed"
	BonusRestored  = "restored"
)

// BonusSlot maps to membership.bonus_slots.
type BonusSlot struct {
	ID               uuid.UUID  `json:"id"`
	PaidPeriodID     uuid.UUID  `json:"paid_period_id"`
	BonusPoints      int64      `json:"bonus_points"`
	Status           string     `json:"status"`
	QualifyingGiftID *uuid.UUID `json:"qualifying_gift_id,omitempty"`
	AwardedLotID     *uuid.UUID `json:"awarded_lot_id,omitempty"`
	AwardedAt        *time.Time `json:"awarded_at,omitempty"`
	StatusReason     *string    `json:"status_reason,omitempty"`
}

// Membership state shown on the status screen.
const (
	MembershipNone             = "none"
	MembershipPaymentPending   = "payment_pending"
	MembershipAwaitingGift     = "awaiting_gift"
	MembershipPendingComplete  = "pending_completion"
	MembershipBonusUnlocked    = "bonus_unlocked"
	MembershipBonusReversed    = "bonus_reversed"
	MembershipPastDue          = "past_due"
	MembershipNotCurrentlyPaid = "not_currently_paid"
)

// MembershipStatus is GET /customers/me/membership.
type MembershipStatus struct {
	State         string        `json:"state"`
	Message       string        `json:"message"`
	NextAction    string        `json:"next_action"`
	Subscription  *Subscription `json:"subscription,omitempty"`
	Offer         *PlanOffer    `json:"offer,omitempty"`
	CurrentPeriod *PaidPeriod   `json:"current_period,omitempty"`
	// NextChargeAt and NextChargeMinor are the actual next renewal, when
	// one is scheduled.
	NextChargeAt    *time.Time   `json:"next_charge_at,omitempty"`
	NextChargeMinor *int64       `json:"next_charge_minor,omitempty"`
	Periods         []PaidPeriod `json:"periods"`
}

// RewardPolicy maps to points.reward_policies.
type RewardPolicy struct {
	ID                 uuid.UUID  `json:"id"`
	CountryID          uuid.UUID  `json:"country_id"`
	CountryName        string     `json:"country_name,omitempty"`
	Currency           string     `json:"currency,omitempty"`
	Version            int        `json:"version"`
	QualifyingUnit     string     `json:"qualifying_unit"`
	CompletionStatus   string     `json:"completion_status"`
	MinEligibleMinor   int64      `json:"min_eligible_minor"`
	ExcludeFreeGifts   bool       `json:"exclude_free_gifts"`
	BaseMode           string     `json:"base_mode"`
	BaseFixedPoints    int64      `json:"base_fixed_points"`
	BasePointsPerUnit  int        `json:"base_points_per_unit"`
	BaseCapPoints      *int64     `json:"base_cap_points,omitempty"`
	ExpiryDays         *int       `json:"expiry_days,omitempty"`
	Status             string     `json:"status"`
	EffectiveFrom      *time.Time `json:"effective_from,omitempty"`
	CreatedByAdminID   *uuid.UUID `json:"created_by_admin_id,omitempty"`
	SubmittedByAdminID *uuid.UUID `json:"submitted_by_admin_id,omitempty"`
	ApprovedByAdminID  *uuid.UUID `json:"approved_by_admin_id,omitempty"`
	PublishedAt        *time.Time `json:"published_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// Gift qualification statuses.
const (
	GiftPending   = "pending"
	GiftAwarded   = "awarded"
	GiftCancelled = "cancelled"
	GiftReversed  = "reversed"
	GiftReview    = "review"
)

// GiftQualification maps to points.gift_qualifications.
type GiftQualification struct {
	ID            uuid.UUID  `json:"id"`
	OrderID       uuid.UUID  `json:"order_id"`
	OrderNumber   string     `json:"order_number,omitempty"`
	PolicyID      uuid.UUID  `json:"policy_id"`
	AnchorAt      time.Time  `json:"anchor_at"`
	EligibleMinor int64      `json:"eligible_minor"`
	Currency      string     `json:"currency"`
	BasePoints    int64      `json:"base_points"`
	Status        string     `json:"status"`
	BaseLotID     *uuid.UUID `json:"base_lot_id,omitempty"`
	BonusSlotID   *uuid.UUID `json:"bonus_slot_id,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	StatusReason  *string    `json:"status_reason,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// CPLot is one competition-points lot with its buckets.
type CPLot struct {
	ID           uuid.UUID       `json:"id"`
	Source       string          `json:"source"`
	SourceID     uuid.UUID       `json:"source_id"`
	Original     int64           `json:"original"`
	Available    int64           `json:"available"`
	Reserved     int64           `json:"reserved"`
	Consumed     int64           `json:"consumed"`
	Reversed     int64           `json:"reversed"`
	Expired      int64           `json:"expired"`
	ExpiresAt    *time.Time      `json:"expires_at,omitempty"`
	Restrictions json.RawMessage `json:"restrictions"`
	CreatedAt    time.Time       `json:"created_at"`
	// Joined for display: the order that earned a gift or bonus lot.
	OrderNumber *string `json:"order_number,omitempty"`
}

// CPActivity is one transaction on a customer's competition points.
type CPActivity struct {
	ID   uuid.UUID `json:"id"`
	Kind string    `json:"kind"`
	// Delta is the change to available points; Amount is how many points
	// the operation moved (a capture moves held points to spent, so its
	// Delta is 0 and its Amount the cost).
	Delta       int64     `json:"delta"`
	Amount      int64     `json:"amount"`
	Source      string    `json:"source,omitempty"`
	Description string    `json:"description"`
	Reason      *string   `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// CPWallet is GET /customers/me/competition-points.
type CPWallet struct {
	Available int64 `json:"available"`
	Reserved  int64 `json:"reserved"`
	// Pending is estimated from gifts not yet completed; it is not
	// spendable.
	Pending      int64               `json:"pending"`
	ExpiringSoon int64               `json:"expiring_soon"`
	BySource     map[string]int64    `json:"by_source"`
	Lots         []CPLot             `json:"lots"`
	PendingGifts []GiftQualification `json:"pending_gifts"`
	OpenEntries  []CPEntry           `json:"open_entries"`
	Activity     []CPActivity        `json:"activity"`
}

// Entry states.
const (
	EntryReserved  = "reserved"
	EntryCaptured  = "captured"
	EntryReleased  = "released"
	EntryUncertain = "uncertain"
)

// CPEntry maps to competition.cp_entries.
type CPEntry struct {
	ID             uuid.UUID  `json:"id"`
	CompetitionID  uuid.UUID  `json:"competition_id"`
	CompetitionTag string     `json:"competition_title,omitempty"`
	RulesVersion   int        `json:"rules_version"`
	Cost           int64      `json:"cost"`
	Channel        string     `json:"channel"`
	State          string     `json:"state"`
	AttemptID      *uuid.UUID `json:"attempt_id,omitempty"`
	HoldExpiresAt  time.Time  `json:"hold_expires_at"`
	ReservedAt     time.Time  `json:"reserved_at"`
	CapturedAt     *time.Time `json:"captured_at,omitempty"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	ReleaseReason  *string    `json:"release_reason,omitempty"`
	// Replayed is set when a repeated request returned this entry.
	Replayed bool `json:"replayed,omitempty"`
}

// EntryQuote is what the customer confirms before points are reserved. A
// quote reserves nothing.
type EntryQuote struct {
	CompetitionID     uuid.UUID `json:"competition_id"`
	Title             string    `json:"title"`
	RulesVersion      int       `json:"rules_version"`
	Cost              int64     `json:"cost"`
	EligibleBalance   int64     `json:"eligible_balance"`
	TotalBalance      int64     `json:"total_balance"`
	IneligibleBalance int64     `json:"ineligible_balance"`
	AllowedSources    []string  `json:"allowed_sources"`
	CurrentPrizeCents int64     `json:"current_prize_cents"`
	MaxPrizeCents     *int64    `json:"max_prize_cents,omitempty"`
	Currency          string    `json:"currency"`
	EndsAt            time.Time `json:"ends_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	Reference         string    `json:"reference"`
	CanEnter          bool      `json:"can_enter"`
	ReasonCode        string    `json:"reason_code,omitempty"`
	Message           string    `json:"message,omitempty"`
}

// CapabilityApproval maps to core.capability_approvals.
type CapabilityApproval struct {
	ID                 uuid.UUID  `json:"id"`
	Operation          string     `json:"operation"`
	CountryID          uuid.UUID  `json:"country_id"`
	CountryName        string     `json:"country_name,omitempty"`
	Channel            string     `json:"channel"`
	Provider           *string    `json:"provider,omitempty"`
	PointSource        *string    `json:"point_source,omitempty"`
	MinAge             *int       `json:"min_age,omitempty"`
	EvidenceRef        string     `json:"evidence_ref"`
	Notes              *string    `json:"notes,omitempty"`
	Status             string     `json:"status"`
	EffectiveFrom      time.Time  `json:"effective_from"`
	ExpiresAt          time.Time  `json:"expires_at"`
	RequestedByAdminID uuid.UUID  `json:"requested_by_admin_id"`
	ApprovedByAdminID  *uuid.UUID `json:"approved_by_admin_id,omitempty"`
	ApprovedAt         *time.Time `json:"approved_at,omitempty"`
	RevokedAt          *time.Time `json:"revoked_at,omitempty"`
	RevokeReason       *string    `json:"revoke_reason,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	// Current is true when the row is active and inside its window now.
	Current bool `json:"current"`
}

// EmergencyStop maps to core.emergency_stops.
type EmergencyStop struct {
	ID                 uuid.UUID  `json:"id"`
	Scope              string     `json:"scope"`
	CountryID          *uuid.UUID `json:"country_id,omitempty"`
	CountryName        *string    `json:"country_name,omitempty"`
	Reason             string     `json:"reason"`
	ActivatedByAdminID uuid.UUID  `json:"activated_by_admin_id"`
	ActivatedAt        time.Time  `json:"activated_at"`
	LiftedAt           *time.Time `json:"lifted_at,omitempty"`
	LiftReason         *string    `json:"lift_reason,omitempty"`
}

// Decision is the eligibility engine's answer for one operation.
type Decision struct {
	Operation  string `json:"operation"`
	Allowed    bool   `json:"allowed"`
	ReasonCode string `json:"reason_code,omitempty"`
	Message    string `json:"message"`
	NextAction string `json:"next_action"`
	// MinAge is set when the approval needs an age the account must show.
	MinAge *int `json:"min_age,omitempty"`
}

// Eligibility is GET /customers/me/eligibility.
type Eligibility struct {
	CountryID   uuid.UUID  `json:"country_id"`
	CountryName string     `json:"country_name"`
	Channel     string     `json:"channel"`
	Decisions   []Decision `json:"decisions"`
}

// CPReviewCase maps to points.cp_review_cases.
type CPReviewCase struct {
	ID          uuid.UUID       `json:"id"`
	Kind        string          `json:"kind"`
	CustomerID  uuid.UUID       `json:"customer_id"`
	SubjectType string          `json:"subject_type"`
	SubjectID   uuid.UUID       `json:"subject_id"`
	Detail      json.RawMessage `json:"detail"`
	Status      string          `json:"status"`
	Resolution  *string         `json:"resolution,omitempty"`
	ResolvedAt  *time.Time      `json:"resolved_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// CPRunResult counts one pass of the competition-points job.
type CPRunResult struct {
	Qualified        int   `json:"qualified"`
	GiftsAwarded     int   `json:"gifts_awarded"`
	BasePoints       int64 `json:"base_points"`
	BonusesAwarded   int   `json:"bonuses_awarded"`
	BonusPoints      int64 `json:"bonus_points"`
	GiftsCancelled   int   `json:"gifts_cancelled"`
	GiftsReversed    int   `json:"gifts_reversed"`
	CasesOpened      int   `json:"cases_opened"`
	LotsExpired      int   `json:"lots_expired"`
	EntriesReleased  int   `json:"entries_released"`
	EntriesUncertain int   `json:"entries_uncertain"`
}

// CPReconciliation is the scheduled comparison of lots against the ledger.
type CPReconciliation struct {
	LotsChecked     int         `json:"lots_checked"`
	Mismatches      []uuid.UUID `json:"mismatches"`
	OrphanedHolds   int         `json:"orphaned_holds"`
	UncertainStarts int         `json:"uncertain_starts"`
	OldestPendingAt *time.Time  `json:"oldest_pending_at,omitempty"`
}
