package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// Admin controls for competition points: membership offers, reward
// policies, approval records, emergency stops and review cases. Every
// change is audited, and every protected change needs a second admin:
// whoever submits a version or requests an approval cannot approve it.

// ErrInvalidCPConfig is a configuration the admin must correct.
var ErrInvalidCPConfig = errors.New("invalid competition points configuration")

func invalidCP(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidCPConfig, msg) }

func (a AdminActor) cpAudit(action, entityType string) models.AuditEntry {
	id := a.ID
	entry := models.AuditEntry{ActorType: "admin", ActorID: &id, Action: action, EntityType: entityType}
	if a.IP != "" {
		entry.IPAddress = &a.IP
	}
	if a.UserAgent != "" {
		entry.UserAgent = &a.UserAgent
	}
	return entry
}

// AdminRefusal maps repository errors an admin can act on.
func adminVersionErr(err error) error {
	switch {
	case errors.Is(err, repository.ErrVersionSamePerson), errors.Is(err, repository.ErrApprovalSamePerson):
		return cpRefuse(http.StatusForbidden, "SECOND_APPROVER_REQUIRED",
			"A different admin must approve this.", "ask_another_admin")
	case errors.Is(err, repository.ErrVersionNotDraft):
		return cpRefuse(http.StatusConflict, "VERSION_LOCKED",
			"This version has been submitted and can no longer change. Create a new version.", "new_version")
	case errors.Is(err, repository.ErrVersionNotAwaiting), errors.Is(err, repository.ErrVersionBadTransition),
		errors.Is(err, repository.ErrApprovalNotPending), errors.Is(err, repository.ErrStopAlreadyLifted):
		return cpRefuse(http.StatusConflict, "INVALID_TRANSITION", err.Error(), "reload")
	case errors.Is(err, repository.ErrVersionNotFound), errors.Is(err, repository.ErrOfferNotFound),
		errors.Is(err, repository.ErrApprovalNotFound), errors.Is(err, repository.ErrStopNotFound):
		return cpRefuse(http.StatusNotFound, "NOT_FOUND", "Not found.", "reload")
	case errors.Is(err, repository.ErrCountryNotFound):
		return invalidCP("country not found")
	case errors.Is(err, repository.ErrInvalidVersion):
		return invalidCP(err.Error())
	}
	return err
}

// ─── Offers ──────────────────────────────────────────────────────────────

// OfferInput is a membership offer draft.
type OfferInput struct {
	TierCode          string  `json:"tier_code"`
	CountryID         string  `json:"country_id"`
	Name              string  `json:"name"`
	Description       *string `json:"description"`
	Currency          string  `json:"currency"`
	PriceMinor        int64   `json:"price_minor"`
	IntervalUnit      string  `json:"interval_unit"`
	IntervalCount     int     `json:"interval_count"`
	MemberBonusPoints int64   `json:"member_bonus_points"`
	// MemberDiscountBPS is the percentage off the gift price members get, in
	// basis points (100 = 1%). Funded from SendAGift's commission, so it
	// cannot be more than that.
	MemberDiscountBPS int      `json:"member_discount_bps"`
	GiftBenefits      []string `json:"gift_benefits"`
	TermsVersion      string   `json:"terms_version"`
}

var tierCodeRe = regexp.MustCompile(`^[a-z0-9_-]{2,40}$`)
var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

func (in OfferInput) toModel(adminID uuid.UUID, platformFeeBPS int) (*models.PlanOffer, error) {
	country, err := uuid.Parse(strings.TrimSpace(in.CountryID))
	if err != nil {
		return nil, invalidCP("country_id is required")
	}
	o := &models.PlanOffer{
		TierCode: strings.ToLower(strings.TrimSpace(in.TierCode)), CountryID: country,
		Name: strings.TrimSpace(in.Name), Currency: strings.ToUpper(strings.TrimSpace(in.Currency)),
		PriceMinor: in.PriceMinor, IntervalUnit: in.IntervalUnit, IntervalCount: in.IntervalCount,
		MemberBonusPoints: in.MemberBonusPoints, MemberDiscountBPS: in.MemberDiscountBPS,
		TermsVersion:     strings.TrimSpace(in.TermsVersion),
		CreatedByAdminID: &adminID,
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if d != "" {
			o.Description = &d
		}
	}
	switch {
	case !tierCodeRe.MatchString(o.TierCode):
		return nil, invalidCP("tier_code must be 2 to 40 lowercase letters, digits, - or _")
	case o.Name == "" || len(o.Name) > 80:
		return nil, invalidCP("name must be 1 to 80 characters")
	case !currencyRe.MatchString(o.Currency):
		return nil, invalidCP("currency must be a 3-letter ISO code")
	case o.PriceMinor <= 0:
		return nil, invalidCP("price_minor must be positive")
	case o.IntervalUnit != "day" && o.IntervalUnit != "week" && o.IntervalUnit != "month" && o.IntervalUnit != "year":
		return nil, invalidCP("interval_unit must be day, week, month or year")
	case o.IntervalCount < 1 || o.IntervalCount > 365:
		return nil, invalidCP("interval_count must be 1 to 365")
	case o.MemberBonusPoints < 0 || o.MemberBonusPoints > 10_000_000:
		return nil, invalidCP("member_bonus_points (the extra award above ordinary gift points) must be 0 to 10000000")
	case o.TermsVersion == "":
		return nil, invalidCP("terms_version is required")
	case o.MemberDiscountBPS < 0:
		return nil, invalidCP("member_discount_bps cannot be negative")
	case o.MemberDiscountBPS > platformFeeBPS:
		if platformFeeBPS <= 0 {
			return nil, invalidCP("a member discount is funded from the platform commission, which is not set: set STRIPE_PLATFORM_FEE_BPS first")
		}
		return nil, invalidCP(fmt.Sprintf("the member discount (%.2f%%) cannot be more than the platform commission (%.2f%%), which funds it",
			float64(o.MemberDiscountBPS)/100, float64(platformFeeBPS)/100))
	}
	var benefits []string
	for _, b := range in.GiftBenefits {
		if b = strings.TrimSpace(b); b != "" {
			if len(b) > 160 {
				return nil, invalidCP("each gift benefit must be at most 160 characters")
			}
			benefits = append(benefits, b)
		}
	}
	if len(benefits) > 10 {
		return nil, invalidCP("at most 10 gift benefits")
	}
	if benefits == nil {
		benefits = []string{}
	}
	raw, err := json.Marshal(benefits)
	if err != nil {
		return nil, err
	}
	o.GiftBenefits = raw
	return o, nil
}

func (s *CompetitionPointsService) AdminOffers(ctx context.Context) ([]models.PlanOffer, error) {
	return s.repo.ListOffers(ctx)
}

func (s *CompetitionPointsService) CreateOffer(ctx context.Context, admin AdminActor, in OfferInput) (*models.PlanOffer, error) {
	o, err := in.toModel(admin.ID, s.platformFeeBPS)
	if err != nil {
		return nil, err
	}
	out, err := s.repo.SaveOfferDraft(ctx, nil, o, admin.cpAudit("membership_offer.create", "membership_offer"))
	return out, adminVersionErr(err)
}

func (s *CompetitionPointsService) UpdateOffer(ctx context.Context, admin AdminActor, id uuid.UUID, in OfferInput) (*models.PlanOffer, error) {
	o, err := in.toModel(admin.ID, s.platformFeeBPS)
	if err != nil {
		return nil, err
	}
	out, err := s.repo.SaveOfferDraft(ctx, &id, o, admin.cpAudit("membership_offer.update", "membership_offer"))
	return out, adminVersionErr(err)
}

// OfferTransition moves an offer version: submit, approve (publish),
// return_to_draft, suspend, resume or retire.
func (s *CompetitionPointsService) OfferTransition(ctx context.Context, admin AdminActor, id uuid.UUID, action string) (*models.PlanOffer, error) {
	to, err := transitionTarget(action)
	if err != nil {
		return nil, err
	}
	out, err := s.repo.TransitionOffer(ctx, id, to, admin.ID, admin.cpAudit("membership_offer."+action, "membership_offer"))
	return out, adminVersionErr(err)
}

func (s *CompetitionPointsService) DeleteOffer(ctx context.Context, admin AdminActor, id uuid.UUID) error {
	return adminVersionErr(s.repo.DeleteOfferDraft(ctx, id, admin.cpAudit("membership_offer.delete", "membership_offer")))
}

func transitionTarget(action string) (string, error) {
	switch action {
	case "submit":
		return models.VersionAwaitingApproval, nil
	case "approve", "resume":
		return models.VersionPublished, nil
	case "return_to_draft":
		return models.VersionDraft, nil
	case "suspend":
		return models.VersionSuspended, nil
	case "retire":
		return models.VersionRetired, nil
	}
	return "", invalidCP("action must be submit, approve, return_to_draft, suspend, resume or retire")
}

// ─── Reward policies ─────────────────────────────────────────────────────

// PolicyInput is a reward policy draft.
type PolicyInput struct {
	CountryID         string `json:"country_id"`
	MinEligibleMinor  int64  `json:"min_eligible_minor"`
	ExcludeFreeGifts  *bool  `json:"exclude_free_gifts"`
	BaseMode          string `json:"base_mode"`
	BaseFixedPoints   int64  `json:"base_fixed_points"`
	BasePointsPerUnit int    `json:"base_points_per_unit"`
	BaseCapPoints     *int64 `json:"base_cap_points"`
	ExpiryDays        *int   `json:"expiry_days"`
}

func (in PolicyInput) toModel(adminID uuid.UUID) (*models.RewardPolicy, error) {
	country, err := uuid.Parse(strings.TrimSpace(in.CountryID))
	if err != nil {
		return nil, invalidCP("country_id is required")
	}
	exclude := true
	if in.ExcludeFreeGifts != nil {
		exclude = *in.ExcludeFreeGifts
	}
	p := &models.RewardPolicy{
		CountryID: country, MinEligibleMinor: in.MinEligibleMinor, ExcludeFreeGifts: exclude,
		BaseMode: in.BaseMode, BaseCapPoints: in.BaseCapPoints, ExpiryDays: in.ExpiryDays,
		CreatedByAdminID: &adminID,
	}
	switch in.BaseMode {
	case "fixed":
		if in.BaseFixedPoints < 1 || in.BaseFixedPoints > 10_000_000 {
			return nil, invalidCP("base_fixed_points must be 1 to 10000000 for a fixed award")
		}
		p.BaseFixedPoints = in.BaseFixedPoints
	case "per_currency_unit":
		if in.BasePointsPerUnit < 1 || in.BasePointsPerUnit > 10000 {
			return nil, invalidCP("base_points_per_unit must be 1 to 10000")
		}
		p.BasePointsPerUnit = in.BasePointsPerUnit
	default:
		return nil, invalidCP("base_mode must be fixed or per_currency_unit")
	}
	switch {
	case p.MinEligibleMinor < 0:
		return nil, invalidCP("min_eligible_minor cannot be negative")
	case p.BaseCapPoints != nil && *p.BaseCapPoints < 1:
		return nil, invalidCP("base_cap_points must be positive when set")
	case p.ExpiryDays != nil && (*p.ExpiryDays < 1 || *p.ExpiryDays > 3650):
		return nil, invalidCP("expiry_days must be 1 to 3650 when set")
	}
	return p, nil
}

func (s *CompetitionPointsService) AdminPolicies(ctx context.Context) ([]models.RewardPolicy, error) {
	return s.repo.ListPolicies(ctx)
}

func (s *CompetitionPointsService) CreatePolicy(ctx context.Context, admin AdminActor, in PolicyInput) (*models.RewardPolicy, error) {
	p, err := in.toModel(admin.ID)
	if err != nil {
		return nil, err
	}
	out, err := s.repo.SavePolicyDraft(ctx, nil, p, admin.cpAudit("reward_policy.create", "reward_policy"))
	return out, adminVersionErr(err)
}

func (s *CompetitionPointsService) UpdatePolicy(ctx context.Context, admin AdminActor, id uuid.UUID, in PolicyInput) (*models.RewardPolicy, error) {
	p, err := in.toModel(admin.ID)
	if err != nil {
		return nil, err
	}
	out, err := s.repo.SavePolicyDraft(ctx, &id, p, admin.cpAudit("reward_policy.update", "reward_policy"))
	return out, adminVersionErr(err)
}

func (s *CompetitionPointsService) PolicyTransition(ctx context.Context, admin AdminActor, id uuid.UUID, action string) (*models.RewardPolicy, error) {
	to, err := transitionTarget(action)
	if err != nil {
		return nil, err
	}
	out, err := s.repo.TransitionPolicy(ctx, id, to, admin.ID, admin.cpAudit("reward_policy."+action, "reward_policy"))
	return out, adminVersionErr(err)
}

func (s *CompetitionPointsService) DeletePolicy(ctx context.Context, admin AdminActor, id uuid.UUID) error {
	return adminVersionErr(s.repo.DeletePolicyDraft(ctx, id, admin.cpAudit("reward_policy.delete", "reward_policy")))
}

// PolicyPreview shows what an ordinary and a member scenario would earn,
// before a policy is published.
type PolicyPreview struct {
	EligibleMinor   int64 `json:"eligible_minor"`
	Qualifies       bool  `json:"qualifies"`
	BasePoints      int64 `json:"base_points"`
	MemberBonus     int64 `json:"member_bonus"`
	MemberFirstGift int64 `json:"member_first_gift_total"`
}

func (s *CompetitionPointsService) PreviewPolicy(in PolicyInput, eligibleMinor, memberBonus int64, currency string) (*PolicyPreview, error) {
	p, err := in.toModel(uuid.Nil)
	if err != nil {
		return nil, err
	}
	out := &PolicyPreview{EligibleMinor: eligibleMinor, Qualifies: eligibleMinor >= p.MinEligibleMinor}
	if out.Qualifies {
		out.BasePoints = repository.BasePoints(p, eligibleMinor, strings.ToUpper(currency))
		out.MemberBonus = memberBonus
		out.MemberFirstGift = out.BasePoints + memberBonus
	}
	return out, nil
}

// ─── Approvals ───────────────────────────────────────────────────────────

// ApprovalInput is a request to record evidence for one operation.
type ApprovalInput struct {
	Operation     string     `json:"operation"`
	CountryID     string     `json:"country_id"`
	Channel       string     `json:"channel"`
	Provider      *string    `json:"provider"`
	PointSource   *string    `json:"point_source"`
	MinAge        *int       `json:"min_age"`
	EvidenceRef   string     `json:"evidence_ref"`
	Notes         *string    `json:"notes"`
	EffectiveFrom *time.Time `json:"effective_from"`
	ExpiresAt     *time.Time `json:"expires_at"`
}

var providerOperations = map[string]bool{
	models.OpMembershipSale: true, models.OpMembershipRenewal: true,
	models.OpWinnerPayout: true, models.OpSellerCampaignFunding: true,
}

func (s *CompetitionPointsService) AdminApprovals(ctx context.Context) ([]models.CapabilityApproval, error) {
	return s.repo.ListApprovals(ctx)
}

func (s *CompetitionPointsService) RequestApproval(ctx context.Context, admin AdminActor, in ApprovalInput) (*models.CapabilityApproval, error) {
	country, err := uuid.Parse(strings.TrimSpace(in.CountryID))
	if err != nil {
		return nil, invalidCP("country_id is required")
	}
	if _, ok := stopScopeFor[in.Operation]; !ok {
		return nil, invalidCP("operation is not one that needs approval")
	}
	switch in.Channel {
	case "any", "web", "ios", "android", "event":
	default:
		return nil, invalidCP("channel must be any, web, ios, android or event")
	}
	if in.Operation == models.OpGiftPointsEarning && in.Channel != "any" {
		return nil, invalidCP("gift points earning runs on the server: use channel any")
	}
	if providerOperations[in.Operation] {
		if in.Provider == nil || (*in.Provider != "stripe" && *in.Provider != "test") {
			return nil, invalidCP("provider (stripe or test) is required for this operation: a connected account does not approve every operation")
		}
	} else {
		in.Provider = nil
	}
	if in.PointSource != nil {
		switch *in.PointSource {
		case models.LotSourceGift, models.LotSourceMemberBonus, models.LotSourceSponsored, models.LotSourceCompensation:
		default:
			return nil, invalidCP("point_source must be gift, member_bonus, sponsored or compensation")
		}
	}
	if in.MinAge != nil && (*in.MinAge < 13 || *in.MinAge > 25) {
		return nil, invalidCP("min_age must be 13 to 25 when set")
	}
	evidence := strings.TrimSpace(in.EvidenceRef)
	if evidence == "" {
		return nil, invalidCP("evidence_ref is required: a reference to the advice, provider acceptance or review that supports this")
	}
	from := s.now()
	if in.EffectiveFrom != nil {
		from = *in.EffectiveFrom
	}
	if in.ExpiresAt == nil || !in.ExpiresAt.After(from) {
		return nil, invalidCP("expires_at is required and must be after effective_from")
	}
	if in.ExpiresAt.After(from.AddDate(3, 0, 0)) {
		return nil, invalidCP("an approval may last at most 3 years; record fresh evidence after that")
	}
	out, err := s.repo.RequestApproval(ctx, &models.CapabilityApproval{
		Operation: in.Operation, CountryID: country, Channel: in.Channel, Provider: in.Provider,
		PointSource: in.PointSource, MinAge: in.MinAge, EvidenceRef: evidence, Notes: in.Notes,
		EffectiveFrom: from, ExpiresAt: *in.ExpiresAt, RequestedByAdminID: admin.ID,
	}, admin.cpAudit("capability_approval.request", "capability_approval"))
	return out, adminVersionErr(err)
}

func (s *CompetitionPointsService) DecideApproval(ctx context.Context, admin AdminActor, id uuid.UUID, approve bool) (*models.CapabilityApproval, error) {
	action := "capability_approval.reject"
	if approve {
		action = "capability_approval.approve"
	}
	out, err := s.repo.DecideApproval(ctx, id, admin.ID, approve, admin.cpAudit(action, "capability_approval"))
	return out, adminVersionErr(err)
}

func (s *CompetitionPointsService) RevokeApproval(ctx context.Context, admin AdminActor, id uuid.UUID, reason string) (*models.CapabilityApproval, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalidCP("reason is required")
	}
	audit := admin.cpAudit("capability_approval.revoke", "capability_approval")
	audit.Reason = &reason
	out, err := s.repo.RevokeApproval(ctx, id, admin.ID, reason, audit)
	return out, adminVersionErr(err)
}

// ─── Emergency stops ─────────────────────────────────────────────────────

var stopScopes = map[string]bool{
	models.StopMembershipSales: true, models.StopMembershipRenewals: true, models.StopGiftPointsEarning: true,
	models.StopCompetitionEntries: true, models.StopCompetitionStarts: true, models.StopPayouts: true,
	models.StopCampaignCommitments: true,
}

func (s *CompetitionPointsService) AdminStops(ctx context.Context) ([]models.EmergencyStop, error) {
	return s.repo.ListStops(ctx)
}

func (s *CompetitionPointsService) ActivateStop(ctx context.Context, admin AdminActor, scope string, countryID *string, reason string) (*models.EmergencyStop, error) {
	if !stopScopes[scope] {
		return nil, invalidCP("scope is not a known stop")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalidCP("reason is required")
	}
	var country *uuid.UUID
	if countryID != nil && strings.TrimSpace(*countryID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(*countryID))
		if err != nil {
			return nil, invalidCP("country_id is not valid")
		}
		country = &id
	}
	audit := admin.cpAudit("emergency_stop.activate", "emergency_stop")
	audit.Reason = &reason
	out, err := s.repo.ActivateStop(ctx, scope, country, reason, admin.ID, audit)
	return out, adminVersionErr(err)
}

func (s *CompetitionPointsService) LiftStop(ctx context.Context, admin AdminActor, id uuid.UUID, reason string) (*models.EmergencyStop, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, invalidCP("reason is required")
	}
	audit := admin.cpAudit("emergency_stop.lift", "emergency_stop")
	audit.Reason = &reason
	out, err := s.repo.LiftStop(ctx, id, admin.ID, reason, audit)
	return out, adminVersionErr(err)
}

// ─── Operations ──────────────────────────────────────────────────────────

func (s *CompetitionPointsService) AdminCases(ctx context.Context, status string) ([]models.CPReviewCase, error) {
	if status != "" && status != "open" && status != "resolved" {
		return nil, invalidCP("status must be open or resolved")
	}
	return s.repo.ListCases(ctx, status)
}

func (s *CompetitionPointsService) ResolveCase(ctx context.Context, admin AdminActor, id uuid.UUID, resolution string) error {
	resolution = strings.TrimSpace(resolution)
	if resolution == "" {
		return invalidCP("resolution is required")
	}
	err := s.repo.ResolveCase(ctx, id, admin.ID, resolution, admin.cpAudit("cp_case.resolve", "cp_review_case"))
	if errors.Is(err, repository.ErrVersionNotFound) {
		return cpRefuse(http.StatusNotFound, "NOT_FOUND", "That case is not open.", "reload")
	}
	return err
}

func (s *CompetitionPointsService) Reconcile(ctx context.Context) (*models.CPReconciliation, error) {
	return s.repo.ReconcileCompetitionPoints(ctx)
}

// CustomerOverview is a customer's competition points and membership, for
// support.
type CustomerOverview struct {
	Wallet      *models.CPWallet         `json:"wallet"`
	Membership  *models.MembershipStatus `json:"membership"`
	Eligibility *models.Eligibility      `json:"eligibility"`
}

func (s *CompetitionPointsService) AdminCustomer(ctx context.Context, customerID uuid.UUID) (*CustomerOverview, error) {
	w, err := s.Wallet(ctx, customerID)
	if err != nil {
		return nil, err
	}
	m, err := s.Status(ctx, customerID)
	if err != nil {
		return nil, err
	}
	e, err := s.Eligibility(ctx, customerID, "web")
	if err != nil {
		return nil, err
	}
	return &CustomerOverview{Wallet: w, Membership: m, Eligibility: e}, nil
}

// ReleaseSettings shows which commercial values are still unset, so a
// sandbox never passes for production.
type ReleaseSettings struct {
	// PlatformFeeBPS is SendAGift's commission per sale, in basis points: the
	// most a member product discount can be.
	PlatformFeeBPS     int      `json:"platform_fee_bps"`
	HoldMinutes        int      `json:"hold_minutes"`
	RestoreWindowHours float64  `json:"restore_window_hours"`
	MembershipProvider string   `json:"membership_provider"`
	TestPayments       bool     `json:"test_payments"`
	OpenDecisions      []string `json:"open_decisions"`
}

func (s *CompetitionPointsService) Settings() ReleaseSettings {
	return ReleaseSettings{
		PlatformFeeBPS:     s.platformFeeBPS,
		HoldMinutes:        int(s.holdTTL.Minutes()),
		RestoreWindowHours: s.restoreWindow.Hours(),
		MembershipProvider: s.membershipProvider(),
		TestPayments:       s.testPayments,
		// The decisions Appendix B leaves to the owner. They are configured,
		// not built in; the release review records each one.
		OpenDecisions: []string{
			"Tier prices, intervals and member bonus quantities (membership offers)",
			"Ordinary point formula, minimum spend and caps (reward policies)",
			"Period anchor: payment capture time is the implemented proposal",
			"Points expiry and spend order: soonest expiry first is implemented",
			"Restoration window for holds released after expiry (CP_RESTORE_WINDOW_HOURS)",
			"Refund and restoration rules beyond the implemented defaults",
			"Entry costs and caps per contest",
			"Approved countries, channels, providers and age thresholds (approval records)",
		},
	}
}
