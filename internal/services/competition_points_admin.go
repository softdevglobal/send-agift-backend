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
	TierCode      string  `json:"tier_code"`
	CountryID     string  `json:"country_id"`
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	Currency      string  `json:"currency"`
	PriceMinor    int64   `json:"price_minor"`
	IntervalUnit  string  `json:"interval_unit"`
	IntervalCount int     `json:"interval_count"`
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
		MemberDiscountBPS: in.MemberDiscountBPS,
		// Memberships no longer carry a points bonus.
		MemberBonusPoints: 0, BonusTrigger: models.BonusTriggerPayment,
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

// MembersList is the admin view of who is a member and on which package.
type MembersList struct {
	Items    []models.MemberRow          `json:"items"`
	Total    int                         `json:"total"`
	Packages []models.MemberPackageCount `json:"packages"`
}

func (s *CompetitionPointsService) AdminMembers(ctx context.Context, f repository.MemberFilter) (*MembersList, error) {
	switch f.State {
	case "", "current", "ended", "all":
	default:
		return nil, invalidCP("state must be current, ended or all")
	}
	items, total, err := s.repo.ListMembers(ctx, f)
	if err != nil {
		return nil, err
	}
	packages, err := s.repo.MemberPackageCounts(ctx)
	if err != nil {
		return nil, err
	}
	return &MembersList{Items: items, Total: total, Packages: packages}, nil
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
	// An admin publishes a membership package directly: no second
	// approver is needed for it.
	if action == "publish" {
		out, err := s.repo.PublishOffer(ctx, id, admin.ID, admin.cpAudit("membership_offer.publish", "membership_offer"))
		if err != nil {
			return nil, adminVersionErr(err)
		}
		return out, s.openMembershipMarket(ctx, admin, out)
	}
	to, err := transitionTarget(action)
	if err != nil {
		return nil, err
	}
	out, err := s.repo.TransitionOffer(ctx, id, to, admin.ID, admin.cpAudit("membership_offer."+action, "membership_offer"))
	if err != nil {
		return nil, adminVersionErr(err)
	}
	if out.Status == models.VersionPublished {
		return out, s.openMembershipMarket(ctx, admin, out)
	}
	return out, nil
}

// openMembershipMarket records the approvals a published package needs to be
// on sale in its country: membership sales on the web, renewals, and gift
// points earning so the member bonus can be awarded. An admin publishing
// the package is the approval; one that already covers an operation is kept.
func (s *CompetitionPointsService) openMembershipMarket(ctx context.Context, admin AdminActor, o *models.PlanOffer) error {
	provider := s.membershipProvider()
	if provider == "" {
		// No payment provider is configured, so nothing could be sold anyway.
		return nil
	}
	existing, err := s.repo.ListApprovals(ctx)
	if err != nil {
		return err
	}
	needs := []struct {
		op, channel string
		provider    *string
	}{
		{models.OpMembershipSale, "web", &provider},
		{models.OpMembershipRenewal, "any", &provider},
		{models.OpGiftPointsEarning, "any", nil},
	}
	now := s.now()
	expires := now.AddDate(1, 0, 0)
	evidence := fmt.Sprintf("Opened by publishing membership package %q (%s v%d)", o.Name, o.TierCode, o.Version)
	for _, n := range needs {
		if approvalCovers(existing, n.op, o.CountryID, n.channel, n.provider) {
			continue
		}
		if _, err := s.RequestApproval(ctx, admin, ApprovalInput{
			Operation: n.op, CountryID: o.CountryID.String(), Channel: n.channel, Provider: n.provider,
			EvidenceRef: evidence, ApproveNow: true, EffectiveFrom: &now, ExpiresAt: &expires,
		}); err != nil {
			return err
		}
	}
	return nil
}

func approvalCovers(list []models.CapabilityApproval, op string, country uuid.UUID, channel string, provider *string) bool {
	for _, a := range list {
		if !a.Current || a.Operation != op || a.CountryID != country {
			continue
		}
		if a.Channel != "any" && a.Channel != channel {
			continue
		}
		if provider != nil && (a.Provider == nil || *a.Provider != *provider) {
			continue
		}
		return true
	}
	return false
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
	return "", invalidCP("action must be publish, submit, approve, return_to_draft, suspend, resume or retire")
}

// ─── Approvals ───────────────────────────────────────────────────────────

// ApprovalInput is a request to record evidence for one operation.
type ApprovalInput struct {
	Operation   string  `json:"operation"`
	CountryID   string  `json:"country_id"`
	Channel     string  `json:"channel"`
	Provider    *string `json:"provider"`
	PointSource *string `json:"point_source"`
	MinAge      *int    `json:"min_age"`
	EvidenceRef string  `json:"evidence_ref"`
	Notes       *string `json:"notes"`
	// ApproveNow records the approval and approves it in the same step.
	ApproveNow    bool       `json:"approve_now"`
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
	if err != nil {
		return nil, adminVersionErr(err)
	}
	if in.ApproveNow {
		return s.DecideApproval(ctx, admin, out.ID, true)
	}
	return out, nil
}

func (s *CompetitionPointsService) DecideApproval(ctx context.Context, admin AdminActor, id uuid.UUID, approve bool) (*models.CapabilityApproval, error) {
	action := "capability_approval.reject"
	if approve {
		action = "capability_approval.approve"
	}
	// An admin may approve one they recorded themselves.
	out, err := s.repo.DecideApproval(ctx, id, admin.ID, approve, true, admin.cpAudit(action, "capability_approval"))
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

// ReleaseSettings shows which commercial values are still unset, so a
// sandbox never passes for production.
type ReleaseSettings struct {
	// PlatformFeeBPS is SendAGift's commission per sale, in basis points: the
	// most a member product discount can be.
	PlatformFeeBPS     int      `json:"platform_fee_bps"`
	MembershipProvider string   `json:"membership_provider"`
	TestPayments       bool     `json:"test_payments"`
	OpenDecisions      []string `json:"open_decisions"`
}

func (s *CompetitionPointsService) Settings() ReleaseSettings {
	return ReleaseSettings{
		PlatformFeeBPS:     s.platformFeeBPS,
		MembershipProvider: s.membershipProvider(),
		TestPayments:       s.testPayments,
		// Commercial decisions that are configured, not built in.
		OpenDecisions: []string{
			"Membership prices, intervals and member discounts (membership packages)",
			"Approved countries, channels and providers for selling memberships (approval records)",
		},
	}
}
