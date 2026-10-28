package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nyaruka/phonenumbers"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// CompetitionPointsService runs competition points (handover v4):
// eligibility, memberships, the competition-points wallet and the
// reserve-then-capture entry to a skill competition.
//
// Nothing here assumes approval. Every customer operation asks the
// eligibility engine first, and the engine answers "unavailable" unless an
// approval record covers that operation in the customer's country and
// channel and no emergency stop is on.
type CompetitionPointsService struct {
	repo         *repository.CompetitionPointsRepository
	competitions *CompetitionService
	stripe       *StripeClient
	webURL       string
	quoteSecret  []byte
	quoteTTL     time.Duration
	holdTTL      time.Duration
	// restoreWindow keeps units released from an expired lot usable for a
	// while, so a failed start does not destroy them.
	restoreWindow time.Duration
	// platformFeeBPS is SendAGift's commission on each sale. A member
	// discount is funded from it, so it can never be more than this.
	platformFeeBPS int
	// testPayments lets a sandbox confirm a membership payment by hand when
	// no Stripe key is set. Never on in production.
	testPayments bool
	now          func() time.Time
}

// CompetitionPointsConfig is the configuration the service needs.
type CompetitionPointsConfig struct {
	WebURL        string
	QuoteSecret   string
	HoldTTL       time.Duration
	RestoreWindow time.Duration
	TestPayments  bool
	// PlatformFeeBPS caps member product discounts (STRIPE_PLATFORM_FEE_BPS).
	PlatformFeeBPS int
}

func NewCompetitionPointsService(repo *repository.CompetitionPointsRepository, competitions *CompetitionService, stripe *StripeClient, cfg CompetitionPointsConfig) *CompetitionPointsService {
	hold := cfg.HoldTTL
	if hold <= 0 {
		hold = 10 * time.Minute
	}
	return &CompetitionPointsService{
		repo: repo, competitions: competitions, stripe: stripe,
		webURL:         strings.TrimRight(cfg.WebURL, "/"),
		quoteSecret:    []byte(cfg.QuoteSecret),
		quoteTTL:       5 * time.Minute,
		holdTTL:        hold,
		restoreWindow:  cfg.RestoreWindow,
		platformFeeBPS: cfg.PlatformFeeBPS,
		testPayments:   cfg.TestPayments,
		now:            time.Now,
	}
}

// CPRefusal is a customer operation that cannot go ahead, with a stable
// reason code, a safe message and the next action the customer can take.
type CPRefusal struct {
	Status     int
	Code       string
	Message    string
	NextAction string
	Details    map[string]any
}

func (e *CPRefusal) Error() string { return e.Message }

func cpRefuse(status int, code, message, next string) *CPRefusal {
	return &CPRefusal{Status: status, Code: code, Message: message, NextAction: next}
}

func refusalFrom(d models.Decision) *CPRefusal {
	return &CPRefusal{Status: http.StatusForbidden, Code: d.ReasonCode, Message: d.Message, NextAction: d.NextAction}
}

// ReasonAgeRestricted is an account below the approved minimum age.
const ReasonAgeRestricted = "AGE_RESTRICTED"

// Channel names a client. Native apps send X-Client-Channel; a browser
// sends nothing and is "web". The value only narrows what is offered: an
// approval must still exist for the channel named.
func Channel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ios":
		return "ios"
	case "android":
		return "android"
	case "event":
		return "event"
	}
	return "web"
}

// ─── Eligibility engine ──────────────────────────────────────────────────

var stopScopeFor = map[string]string{
	models.OpMembershipSale:        models.StopMembershipSales,
	models.OpMembershipRenewal:     models.StopMembershipRenewals,
	models.OpGiftPointsEarning:     models.StopGiftPointsEarning,
	models.OpCompetitionEntry:      models.StopCompetitionEntries,
	models.OpWinnerPayout:          models.StopPayouts,
	models.OpSellerCampaignFunding: models.StopCampaignCommitments,
}

// needsResidence are operations that must not rest on the account country
// alone: a verified phone number from that country is also required.
var needsResidence = map[string]bool{
	models.OpMembershipSale:   true,
	models.OpCompetitionEntry: true,
	models.OpWinnerPayout:     true,
}

// decide is the eligibility engine for one operation. An error is never an
// approval: a failure to check is reported as unavailable by the caller.
func (s *CompetitionPointsService) decide(ctx context.Context, ev *repository.CustomerEvidence, op, channel string, provider *string) (models.Decision, []models.CapabilityApproval, error) {
	d := models.Decision{Operation: op}
	if ev.Deleted || ev.Status != "active" {
		d.ReasonCode, d.Message, d.NextAction = models.ReasonVerificationRequired,
			"Your account is not active.", "contact_support"
		return d, nil, nil
	}
	if scope, ok := stopScopeFor[op]; ok {
		stopped, err := s.repo.StopActive(ctx, scope, ev.CountryID)
		if err != nil {
			return d, nil, err
		}
		if stopped {
			d.ReasonCode, d.Message, d.NextAction = models.ReasonOperationStopped,
				"This is paused right now. Please check back later.", "shop"
			return d, nil, nil
		}
	}
	approvals, err := s.repo.CurrentApprovals(ctx, repository.ApprovalQuery{
		Operation: op, CountryID: ev.CountryID, Channel: channel, Provider: provider,
	})
	if err != nil {
		return d, nil, err
	}
	if len(approvals) == 0 {
		d.ReasonCode, d.NextAction = models.ReasonLocationUnavailable, "shop"
		d.Message = "This isn't available in " + ev.CountryName + " yet. Gift shopping is unaffected."
		if channel != "web" {
			web, err := s.repo.CurrentApprovals(ctx, repository.ApprovalQuery{
				Operation: op, CountryID: ev.CountryID, Channel: "web", Provider: provider,
			})
			if err != nil {
				return d, nil, err
			}
			if len(web) > 0 {
				d.ReasonCode, d.NextAction = models.ReasonChannelUnavailable, "view_details"
				d.Message = "This isn't available in this app."
			}
		}
		return d, nil, nil
	}
	if needsResidence[op] && !residentBy(ev) {
		d.ReasonCode, d.NextAction = models.ReasonVerificationRequired, "verify_phone"
		d.Message = "Verify a mobile number from " + ev.CountryName + " to confirm where you live."
		return d, approvals, nil
	}
	if minAge := minAgeOf(approvals); minAge != nil {
		d.MinAge = minAge
		if ev.DateOfBirth == nil {
			d.ReasonCode, d.NextAction = models.ReasonVerificationRequired, "add_date_of_birth"
			d.Message = fmt.Sprintf("Add your date of birth: you must be %d or older.", *minAge)
			return d, approvals, nil
		}
		if age(*ev.DateOfBirth, s.now()) < *minAge {
			d.ReasonCode, d.NextAction = ReasonAgeRestricted, "shop"
			d.Message = fmt.Sprintf("You must be %d or older.", *minAge)
			return d, approvals, nil
		}
	}
	d.Allowed = true
	d.Message = "Available."
	return d, approvals, nil
}

// residentBy is the residence evidence rule: a verified phone number whose
// number belongs to the account's country.
func residentBy(ev *repository.CustomerEvidence) bool {
	if ev.PhoneVerifiedAt == nil || ev.PhoneE164 == nil {
		return false
	}
	num, err := phonenumbers.Parse(*ev.PhoneE164, "")
	if err != nil {
		return false
	}
	return strings.EqualFold(phonenumbers.GetRegionCodeForNumber(num), ev.CountryISO)
}

// minAgeOf is the lowest age any of the approvals requires; nil if one of
// them requires none.
func minAgeOf(approvals []models.CapabilityApproval) *int {
	var lowest *int
	for _, a := range approvals {
		if a.MinAge == nil {
			return nil
		}
		if lowest == nil || *a.MinAge < *lowest {
			v := *a.MinAge
			lowest = &v
		}
	}
	return lowest
}

func age(dob, now time.Time) int {
	years := now.Year() - dob.Year()
	if now.YearDay() < dob.YearDay() {
		years--
	}
	return years
}

// approvedSources are the lot sources the approvals allow; nil means all.
func approvedSources(approvals []models.CapabilityApproval) []string {
	var out []string
	for _, a := range approvals {
		if a.PointSource == nil {
			return nil
		}
		if !slices.Contains(out, *a.PointSource) {
			out = append(out, *a.PointSource)
		}
	}
	return out
}

func intersectSources(round, approved []string) []string {
	if approved == nil {
		return round
	}
	var out []string
	for _, s := range round {
		if slices.Contains(approved, s) {
			out = append(out, s)
		}
	}
	return out
}

// Eligibility is GET /customers/me/eligibility.
func (s *CompetitionPointsService) Eligibility(ctx context.Context, customerID uuid.UUID, channel string) (*models.Eligibility, error) {
	ev, err := s.repo.Evidence(ctx, customerID)
	if err != nil {
		return nil, err
	}
	out := &models.Eligibility{CountryID: ev.CountryID, CountryName: ev.CountryName, Channel: channel}
	for _, op := range []string{models.OpMembershipSale, models.OpGiftPointsEarning, models.OpCompetitionEntry} {
		d, _, err := s.decide(ctx, ev, op, channel, s.providerFor(op))
		if err != nil {
			return nil, err
		}
		out.Decisions = append(out.Decisions, d)
	}
	return out, nil
}

// providerFor is the payment provider an operation would use now.
func (s *CompetitionPointsService) providerFor(op string) *string {
	if op != models.OpMembershipSale && op != models.OpMembershipRenewal {
		return nil
	}
	p := s.membershipProvider()
	if p == "" {
		return nil
	}
	return &p
}

func (s *CompetitionPointsService) membershipProvider() string {
	switch {
	case s.stripe != nil:
		return "stripe"
	case s.testPayments:
		return "test"
	}
	return ""
}

// EarningGate is the reward engine's check: an approval for gift earning in
// the country (any channel) and no stop.
func (s *CompetitionPointsService) EarningGate(ctx context.Context, countryID uuid.UUID) (bool, error) {
	stopped, err := s.repo.StopActive(ctx, models.StopGiftPointsEarning, countryID)
	if err != nil || stopped {
		return false, err
	}
	approvals, err := s.repo.CurrentApprovals(ctx, repository.ApprovalQuery{
		Operation: models.OpGiftPointsEarning, CountryID: countryID, Channel: "any",
	})
	return len(approvals) > 0, err
}

// ─── Membership ──────────────────────────────────────────────────────────

// MembershipOffers is what the membership page shows: the decision, and the
// offers on sale in the customer's country when it allows a sale.
type MembershipOffers struct {
	Decision models.Decision    `json:"decision"`
	Offers   []models.PlanOffer `json:"offers"`
}

func (s *CompetitionPointsService) Offers(ctx context.Context, customerID uuid.UUID, channel string) (*MembershipOffers, error) {
	ev, err := s.repo.Evidence(ctx, customerID)
	if err != nil {
		return nil, err
	}
	d, _, err := s.decide(ctx, ev, models.OpMembershipSale, channel, s.providerFor(models.OpMembershipSale))
	if err != nil {
		return nil, err
	}
	if s.membershipProvider() == "" && d.Allowed {
		d.Allowed, d.ReasonCode, d.NextAction = false, models.ReasonProviderUnavailable, "shop"
		d.Message = "Memberships can't be bought right now."
	}
	out := &MembershipOffers{Decision: d, Offers: []models.PlanOffer{}}
	// Offers are shown when a sale is possible or only needs the customer
	// to verify something; never as the remedy for a location restriction.
	if d.Allowed || d.ReasonCode == models.ReasonVerificationRequired {
		if out.Offers, err = s.repo.PublishedOffers(ctx, ev.CountryID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// JoinInput is a customer's request to join.
type JoinInput struct {
	OfferID uuid.UUID
	// RenewalConsent must be true: the customer agreed to the renewal
	// amount and interval shown. It is never pre-selected.
	RenewalConsent bool
	IdempotencyKey string
}

// JoinResult is where to pay.
type JoinResult struct {
	Subscription *models.Subscription `json:"subscription"`
	CheckoutURL  string               `json:"checkout_url"`
	Provider     string               `json:"provider"`
}

// Join starts a membership checkout. The membership only becomes active
// when the provider confirms the first payment.
func (s *CompetitionPointsService) Join(ctx context.Context, customerID uuid.UUID, channel string, in JoinInput) (*JoinResult, error) {
	if !in.RenewalConsent {
		return nil, cpRefuse(http.StatusBadRequest, "RENEWAL_CONSENT_REQUIRED",
			"Please confirm you agree to the renewal price and interval.", "review_terms")
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" || len(key) > 120 {
		return nil, cpRefuse(http.StatusBadRequest, models.ReasonIdempotencyKeyConflict,
			"An Idempotency-Key of 1 to 120 characters is required.", "retry")
	}
	ev, err := s.repo.Evidence(ctx, customerID)
	if err != nil {
		return nil, err
	}
	provider := s.membershipProvider()
	if provider == "" {
		return nil, cpRefuse(http.StatusServiceUnavailable, models.ReasonProviderUnavailable,
			"Memberships can't be bought right now.", "shop")
	}
	d, _, err := s.decide(ctx, ev, models.OpMembershipSale, channel, &provider)
	if err != nil {
		return nil, err
	}
	if !d.Allowed {
		return nil, refusalFrom(d)
	}
	offer, err := s.repo.GetOffer(ctx, in.OfferID)
	if errors.Is(err, repository.ErrOfferNotFound) {
		return nil, cpRefuse(http.StatusNotFound, models.ReasonNoOffer, "That membership is not available.", "view_offers")
	}
	if err != nil {
		return nil, err
	}
	if offer.Status != models.VersionPublished || offer.CountryID != ev.CountryID ||
		offer.EffectiveFrom == nil || offer.EffectiveFrom.After(s.now()) {
		return nil, cpRefuse(http.StatusConflict, models.ReasonNoOffer,
			"That membership is no longer on sale. Please choose again.", "view_offers")
	}
	sub, created, err := s.repo.StartSubscription(ctx, customerID, offer.ID, provider, key, s.now())
	switch {
	case errors.Is(err, repository.ErrAlreadyMember):
		return nil, cpRefuse(http.StatusConflict, models.ReasonAlreadyMember,
			"You already have a membership.", "view_membership")
	case errors.Is(err, repository.ErrKeyReused):
		return nil, cpRefuse(http.StatusConflict, models.ReasonIdempotencyKeyConflict,
			"This request key was already used for a different membership.", "retry")
	case err != nil:
		return nil, err
	}
	if !created && sub.CheckoutURL != nil {
		return &JoinResult{Subscription: sub, CheckoutURL: *sub.CheckoutURL, Provider: provider}, nil
	}
	if sub.Status != models.SubscriptionIncomplete {
		return nil, cpRefuse(http.StatusConflict, models.ReasonAlreadyMember, "You already have a membership.", "view_membership")
	}
	var url, sessionID string
	switch provider {
	case "stripe":
		cs, err := s.stripe.CreateSubscriptionCheckout(ctx, SubscriptionCheckoutParams{
			SubscriptionID: sub.ID.String(), OfferID: offer.ID.String(),
			OfferName: "SendAGift " + offer.Name,
			Currency:  offer.Currency, PriceMinor: offer.PriceMinor,
			IntervalUnit: offer.IntervalUnit, IntervalCount: offer.IntervalCount,
			CustomerEmail: ev.Email,
			SuccessURL:    s.webURL + "/account/points?welcome=membership",
			CancelURL:     s.webURL + "/membership?checkout=cancelled",
		}, "membership-checkout-"+sub.ID.String())
		if err != nil {
			_ = s.repo.AbandonSubscription(ctx, sub.ID)
			log.Printf("membership checkout %s: %v", sub.ID, err)
			return nil, cpRefuse(http.StatusBadGateway, models.ReasonProviderUnavailable,
				"We couldn't open the payment page. Please try again.", "retry")
		}
		url, sessionID = cs.URL, cs.ID
	case "test":
		url, sessionID = s.webURL+"/account/membership?test_checkout="+sub.ID.String(), "test-"+sub.ID.String()
	}
	if err := s.repo.SetSubscriptionCheckout(ctx, sub.ID, sessionID, url); err != nil {
		return nil, err
	}
	sub.CheckoutURL = &url
	return &JoinResult{Subscription: sub, CheckoutURL: url, Provider: provider}, nil
}

// TestPay confirms a sandbox membership payment for the current period.
// Only with MEMBERSHIP_TEST_PAYMENTS and no Stripe key.
func (s *CompetitionPointsService) TestPay(ctx context.Context, customerID uuid.UUID) (*models.MembershipStatus, error) {
	if !s.testPayments || s.stripe != nil {
		return nil, cpRefuse(http.StatusNotFound, models.ReasonProviderUnavailable, "Test payments are off.", "")
	}
	sub, err := s.repo.LiveSubscription(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.Provider != "test" {
		return nil, cpRefuse(http.StatusNotFound, "NO_MEMBERSHIP", "There is no test membership to pay for.", "view_offers")
	}
	offer, err := s.repo.GetOffer(ctx, sub.OfferID)
	if err != nil {
		return nil, err
	}
	start := s.now().UTC()
	if sub.CurrentPeriodEnd != nil && sub.CurrentPeriodEnd.After(start) {
		start = *sub.CurrentPeriodEnd
	}
	end := addInterval(start, offer.IntervalUnit, offer.IntervalCount)
	providerSub := "test-sub-" + sub.ID.String()
	if _, err := s.repo.ApplyProviderSubscription(ctx, repository.ProviderSubscriptionUpdate{
		Provider: "test", ProviderSubscriptionID: providerSub, LocalID: &sub.ID,
		ProviderStatus: "active", CancelAtPeriodEnd: sub.CancelAtPeriodEnd, CurrentPeriodEnd: &end,
	}); err != nil {
		return nil, err
	}
	if _, err := s.repo.RecordPaidPeriod(ctx, repository.PaidInvoice{
		Provider: "test", ProviderSubscriptionID: providerSub, LocalID: &sub.ID,
		InvoiceID: "test-inv-" + start.Format(time.RFC3339), PeriodStart: start, PeriodEnd: end,
		AmountMinor: offer.PriceMinor, Currency: offer.Currency, PaidAt: s.now().UTC(),
	}); err != nil {
		return nil, err
	}
	return s.Status(ctx, customerID)
}

func addInterval(t time.Time, unit string, n int) time.Time {
	switch unit {
	case "day":
		return t.AddDate(0, 0, n)
	case "week":
		return t.AddDate(0, 0, 7*n)
	case "year":
		return t.AddDate(n, 0, 0)
	}
	return t.AddDate(0, n, 0)
}

// Cancel stops renewal at the end of the period already paid for.
func (s *CompetitionPointsService) Cancel(ctx context.Context, customerID uuid.UUID) (*models.MembershipStatus, error) {
	sub, err := s.repo.LiveSubscription(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, cpRefuse(http.StatusNotFound, "NO_MEMBERSHIP", "You don't have a membership to cancel.", "view_offers")
	}
	if err := s.repo.RequestCancel(ctx, sub.ID); err != nil {
		return nil, err
	}
	switch {
	case sub.ProviderSubscriptionID == nil:
		// Never paid: there is nothing to renew.
		if err := s.repo.AbandonSubscription(ctx, sub.ID); err != nil {
			return nil, err
		}
	case sub.Provider == "stripe":
		if s.stripe == nil {
			return nil, cpRefuse(http.StatusServiceUnavailable, models.ReasonProviderUnavailable,
				"We couldn't reach the payment provider. Please try again.", "retry")
		}
		stripeSub, err := s.stripe.CancelSubscriptionAtPeriodEnd(ctx, *sub.ProviderSubscriptionID,
			"membership-cancel-"+sub.ID.String())
		if err != nil {
			return nil, cpRefuse(http.StatusBadGateway, models.ReasonProviderUnavailable,
				"We couldn't cancel with the payment provider. Please try again.", "retry")
		}
		if _, err := s.applyStripeSubscription(ctx, stripeSub); err != nil {
			return nil, err
		}
	default:
		if _, err := s.repo.ApplyProviderSubscription(ctx, repository.ProviderSubscriptionUpdate{
			Provider: sub.Provider, ProviderSubscriptionID: *sub.ProviderSubscriptionID, LocalID: &sub.ID,
			ProviderStatus: "active", CancelAtPeriodEnd: true,
		}); err != nil {
			return nil, err
		}
	}
	return s.Status(ctx, customerID)
}

// Resume turns renewal back on for a membership set to end at its period
// end. It renews again only where renewals are still approved.
func (s *CompetitionPointsService) Resume(ctx context.Context, customerID uuid.UUID, channel string) (*models.MembershipStatus, error) {
	sub, err := s.repo.LiveSubscription(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if sub == nil || sub.ProviderSubscriptionID == nil || !sub.CancelAtPeriodEnd ||
		(sub.Status != models.SubscriptionCancelling && sub.Status != models.SubscriptionActive) {
		return nil, cpRefuse(http.StatusConflict, "NOTHING_TO_RESUME",
			"There is no ending membership to turn renewal back on for.", "view_membership")
	}
	ev, err := s.repo.Evidence(ctx, customerID)
	if err != nil {
		return nil, err
	}
	provider := sub.Provider
	d, _, err := s.decide(ctx, ev, models.OpMembershipRenewal, channel, &provider)
	if err != nil {
		return nil, err
	}
	if !d.Allowed {
		return nil, refusalFrom(d)
	}
	switch sub.Provider {
	case "stripe":
		if s.stripe == nil {
			return nil, cpRefuse(http.StatusServiceUnavailable, models.ReasonProviderUnavailable,
				"We couldn't reach the payment provider. Please try again.", "retry")
		}
		stripeSub, err := s.stripe.ResumeSubscription(ctx, *sub.ProviderSubscriptionID,
			"membership-resume-"+sub.ID.String()+"-"+s.now().UTC().Format("20060102150405"))
		if err != nil {
			return nil, cpRefuse(http.StatusBadGateway, models.ReasonProviderUnavailable,
				"We couldn't turn renewal back on with the payment provider. Please try again.", "retry")
		}
		if _, err := s.applyStripeSubscription(ctx, stripeSub); err != nil {
			return nil, err
		}
	default:
		if _, err := s.repo.ApplyProviderSubscription(ctx, repository.ProviderSubscriptionUpdate{
			Provider: sub.Provider, ProviderSubscriptionID: *sub.ProviderSubscriptionID, LocalID: &sub.ID,
			ProviderStatus: "active", CancelAtPeriodEnd: false,
		}); err != nil {
			return nil, err
		}
	}
	if err := s.repo.ClearCancelRequest(ctx, sub.ID); err != nil {
		return nil, err
	}
	return s.Status(ctx, customerID)
}

// Status is GET /customers/me/membership: the period in force, its bonus
// and the actual next charge.
func (s *CompetitionPointsService) Status(ctx context.Context, customerID uuid.UUID) (*models.MembershipStatus, error) {
	sub, err := s.repo.LatestSubscription(ctx, customerID)
	if err != nil {
		return nil, err
	}
	periods, err := s.repo.Periods(ctx, customerID, 24)
	if err != nil {
		return nil, err
	}
	st := &models.MembershipStatus{State: models.MembershipNone, Subscription: sub, Periods: periods,
		Message: "You're not a member.", NextAction: "view_offers"}
	if sub == nil {
		return st, nil
	}
	if st.Offer, err = s.repo.GetOffer(ctx, sub.OfferID); err != nil {
		return nil, err
	}
	now := s.now()
	for i := range periods {
		p := periods[i]
		if p.Status == "paid" && !now.Before(p.PeriodStart) && now.Before(p.PeriodEnd) {
			st.CurrentPeriod = &periods[i]
			break
		}
	}
	switch {
	case sub.Status == models.SubscriptionIncomplete:
		st.State, st.Message, st.NextAction = models.MembershipPaymentPending,
			"We're waiting for your payment to be confirmed.", "check_status"
	case st.CurrentPeriod == nil && sub.Status == models.SubscriptionPastDue:
		st.State, st.Message, st.NextAction = models.MembershipPastDue,
			"Your renewal payment didn't go through. Update your payment method to keep your benefits.", "manage_renewal"
	case st.CurrentPeriod == nil:
		st.State, st.Message, st.NextAction = models.MembershipNotCurrentlyPaid,
			"You don't have a paid membership period right now.", "view_offers"
	default:
		slot := st.CurrentPeriod.BonusSlot
		switch {
		case slot == nil:
			st.State, st.Message, st.NextAction = models.MembershipAwaitingGift,
				"Your membership is active. Gifts earn ordinary points.", "send_gift"
		case (slot.Status == models.BonusAwarded || slot.Status == models.BonusRestored) && slot.QualifyingGiftID == nil:
			st.State, st.Message, st.NextAction = models.MembershipBonusUnlocked,
				fmt.Sprintf("Your member bonus of %d points for this period is in My points.", slot.BonusPoints),
				"view_points"
		case slot.Status == models.BonusAwarded || slot.Status == models.BonusRestored:
			st.State, st.Message, st.NextAction = models.MembershipBonusUnlocked,
				"Member bonus awarded for this period. Further gifts earn ordinary points.", "view_points"
		case slot.Status == models.BonusReversed && slot.QualifyingGiftID == nil:
			st.State, st.Message, st.NextAction = models.MembershipBonusReversed,
				"The payment for this period was refunded, so its bonus was taken back.", "contact_support"
		case slot.Status == models.BonusReversed:
			st.State, st.Message, st.NextAction = models.MembershipBonusReversed,
				"The gift that unlocked this period's bonus was refunded, so the bonus was taken back.", "contact_support"
		default:
			pending, err := s.repo.HasPendingGiftInPeriod(ctx, customerID, st.CurrentPeriod.PeriodStart, st.CurrentPeriod.PeriodEnd)
			if err != nil {
				return nil, err
			}
			if pending {
				st.State, st.Message, st.NextAction = models.MembershipPendingComplete,
					"Your bonus unlocks when your gift is delivered. Track your gift.", "track_gift"
			} else {
				st.State, st.Message, st.NextAction = models.MembershipAwaitingGift,
					"Your member bonus is ready to unlock. Send a qualifying gift.", "send_gift"
			}
		}
	}
	if (sub.Status == models.SubscriptionActive || sub.Status == models.SubscriptionPastDue) &&
		!sub.CancelAtPeriodEnd && sub.CurrentPeriodEnd != nil {
		next := *sub.CurrentPeriodEnd
		st.NextChargeAt = &next
		offer := st.Offer
		if sub.ScheduledOfferID != nil {
			if scheduled, err := s.repo.GetOffer(ctx, *sub.ScheduledOfferID); err == nil {
				offer = scheduled
			}
		}
		amount := offer.PriceMinor
		st.NextChargeMinor = &amount
	}
	return st, nil
}

// ─── Stripe events ───────────────────────────────────────────────────────

// HandleStripeEvent applies the Stripe events that belong to memberships.
// handled is true when the event was a membership's and nothing else
// should act on it.
func (s *CompetitionPointsService) HandleStripeEvent(ctx context.Context, ev *StripeEvent) (handled bool, err error) {
	switch ev.Type {
	case "checkout.session.completed", "checkout.session.expired":
		var cs struct {
			ID           string            `json:"id"`
			Subscription string            `json:"subscription"`
			Customer     string            `json:"customer"`
			Metadata     map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(ev.Data.Object, &cs); err != nil {
			return false, err
		}
		if cs.Metadata["kind"] != membershipCheckoutKind {
			return false, nil
		}
		localID, _ := uuid.Parse(cs.Metadata["membership_subscription_id"])
		if ev.Type == "checkout.session.expired" || cs.Subscription == "" {
			if localID != uuid.Nil {
				return true, s.repo.AbandonSubscription(ctx, localID)
			}
			return true, nil
		}
		status := "incomplete"
		var periodEnd *time.Time
		cancel := false
		if s.stripe != nil {
			if sub, err := s.stripe.GetSubscription(ctx, cs.Subscription); err == nil {
				status, periodEnd, cancel = sub.Status, sub.PeriodEnd(), sub.CancelAtPeriodEnd
			}
		}
		u := repository.ProviderSubscriptionUpdate{
			Provider: "stripe", ProviderSubscriptionID: cs.Subscription, CheckoutSessionID: &cs.ID,
			ProviderCustomerID: nonEmpty(cs.Customer), ProviderStatus: status, CurrentPeriodEnd: periodEnd,
			CancelAtPeriodEnd: cancel,
		}
		if localID != uuid.Nil {
			u.LocalID = &localID
		}
		_, err := s.repo.ApplyProviderSubscription(ctx, u)
		return true, ignoreUnknownSubscription(err)
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted":
		var sub StripeSubscription
		if err := json.Unmarshal(ev.Data.Object, &sub); err != nil {
			return false, err
		}
		if sub.Metadata["kind"] != membershipCheckoutKind {
			return false, nil
		}
		if ev.Type == "customer.subscription.deleted" {
			sub.Status = "canceled"
		}
		_, err := s.applyStripeSubscription(ctx, &sub)
		return true, ignoreUnknownSubscription(err)
	case "invoice.paid", "invoice.payment_succeeded":
		inv, err := decodeInvoice(ev.Data.Object)
		if err != nil {
			return false, err
		}
		return s.recordStripeInvoice(ctx, inv, nil)
	case "charge.refunded", "charge.dispute.created":
		var ch struct {
			ID            string `json:"id"`
			Charge        string `json:"charge"`
			PaymentIntent string `json:"payment_intent"`
			Invoice       string `json:"invoice"`
			Refunded      bool   `json:"refunded"`
		}
		if err := json.Unmarshal(ev.Data.Object, &ch); err != nil {
			return false, err
		}
		status := "refunded"
		if ev.Type == "charge.dispute.created" {
			status = "disputed"
		} else if !ch.Refunded {
			return false, nil // a partial refund leaves the period paid
		}
		for _, ref := range []string{ch.PaymentIntent, ch.Invoice, ch.Charge, ch.ID} {
			if ref == "" {
				continue
			}
			changed, err := s.repo.SetPeriodPaymentStatus(ctx, "stripe", ref, status)
			if err != nil || changed {
				// Orders may still act on a refund; returning false lets them.
				return false, err
			}
		}
		return false, nil
	}
	return false, nil
}

// recordStripeInvoice turns a paid membership invoice into a paid period.
// handled is false for an invoice that is not a membership's.
func (s *CompetitionPointsService) recordStripeInvoice(ctx context.Context, inv *StripeInvoice, localID *uuid.UUID) (handled bool, err error) {
	meta := inv.Metadata()
	if inv.SubscriptionID() == "" || (meta["kind"] != "" && meta["kind"] != membershipCheckoutKind) {
		return false, nil
	}
	start, end, ok := inv.Period()
	if !ok || inv.Status != "paid" {
		return true, nil
	}
	paidAt := s.now().UTC()
	if t := unixTime(inv.StatusTransitions.PaidAt); t != nil {
		paidAt = *t
	}
	payment := nonEmpty(inv.PaymentIntent)
	if payment == nil {
		payment = nonEmpty(inv.Charge)
	}
	in := repository.PaidInvoice{
		Provider: "stripe", ProviderSubscriptionID: inv.SubscriptionID(), InvoiceID: inv.ID, PaymentID: payment,
		PeriodStart: start, PeriodEnd: end, AmountMinor: inv.AmountPaid, Currency: strings.ToUpper(inv.Currency),
		PaidAt: paidAt, LocalID: localID,
	}
	if id, err := uuid.Parse(meta["membership_subscription_id"]); err == nil {
		in.LocalID = &id
	}
	_, err = s.repo.RecordPaidPeriod(ctx, in)
	if errors.Is(err, repository.ErrSubscriptionNotFound) {
		return meta["kind"] == membershipCheckoutKind, nil // not one of ours
	}
	return err == nil, err
}

// SyncMembership is POST /customers/me/membership/sync: the page Stripe
// returns to asks Stripe directly whether the payment went through, so a
// paid membership never waits on a webhook.
func (s *CompetitionPointsService) SyncMembership(ctx context.Context, customerID uuid.UUID) (*models.MembershipStatus, error) {
	if s.stripe != nil {
		subs, err := s.repo.StripeSubscriptionsToSync(ctx, &customerID, 1)
		if err != nil {
			return nil, err
		}
		for _, sub := range subs {
			if err := s.syncStripeSubscription(ctx, sub); err != nil {
				log.Printf("membership sync %s: %v", sub.ID, err)
			}
		}
	}
	return s.Status(ctx, customerID)
}

// CurrentStatus is GET /customers/me/membership. While a payment is still
// being confirmed it asks Stripe first, so the page never waits on a
// webhook or the background job.
func (s *CompetitionPointsService) CurrentStatus(ctx context.Context, customerID uuid.UUID) (*models.MembershipStatus, error) {
	st, err := s.Status(ctx, customerID)
	if err != nil || s.stripe == nil || st.Subscription == nil || st.Subscription.Provider != "stripe" {
		return st, err
	}
	if st.State == models.MembershipPaymentPending ||
		(st.CurrentPeriod == nil && st.Subscription.Status == models.SubscriptionActive) {
		return s.SyncMembership(ctx, customerID)
	}
	return st, nil
}

// syncStripeMemberships is the background job's check for memberships whose
// webhooks did not arrive.
func (s *CompetitionPointsService) syncStripeMemberships(ctx context.Context) error {
	if s.stripe == nil {
		return nil
	}
	subs, err := s.repo.StripeSubscriptionsToSync(ctx, nil, 20)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if err := s.syncStripeSubscription(ctx, sub); err != nil {
			log.Printf("membership sync %s: %v", sub.ID, err)
		}
		if err := s.repo.TouchSubscription(ctx, sub.ID); err != nil {
			return err
		}
	}
	return nil
}

// syncStripeSubscription reads one membership's checkout, subscription and
// paid invoices from Stripe and applies them, exactly as their webhooks
// would. Applying them twice changes nothing.
func (s *CompetitionPointsService) syncStripeSubscription(ctx context.Context, sub repository.SubscriptionSync) error {
	providerSub := ""
	if sub.ProviderSubscriptionID != nil {
		providerSub = *sub.ProviderSubscriptionID
	}
	if providerSub == "" {
		if sub.CheckoutSessionID == nil {
			return nil
		}
		cs, err := s.stripe.GetCheckoutSession(ctx, *sub.CheckoutSessionID)
		if err != nil {
			return err
		}
		if cs.Status == "expired" {
			return s.repo.AbandonSubscription(ctx, sub.ID)
		}
		if cs.Subscription == "" {
			return nil // not paid yet
		}
		providerSub = cs.Subscription
		if _, err := s.repo.ApplyProviderSubscription(ctx, repository.ProviderSubscriptionUpdate{
			Provider: "stripe", ProviderSubscriptionID: providerSub, LocalID: &sub.ID, CheckoutSessionID: &cs.ID,
			ProviderCustomerID: nonEmpty(cs.Customer), ProviderStatus: "incomplete",
		}); err != nil {
			return err
		}
	}
	stripeSub, err := s.stripe.GetSubscription(ctx, providerSub)
	if err != nil {
		return err
	}
	if stripeSub.Metadata == nil {
		stripeSub.Metadata = map[string]string{}
	}
	stripeSub.Metadata["membership_subscription_id"] = sub.ID.String()
	if _, err := s.applyStripeSubscription(ctx, stripeSub); err != nil {
		return err
	}
	invoices, err := s.stripe.PaidInvoices(ctx, providerSub)
	if err != nil {
		return err
	}
	for i := range invoices {
		if _, err := s.recordStripeInvoice(ctx, &invoices[i], &sub.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *CompetitionPointsService) applyStripeSubscription(ctx context.Context, sub *StripeSubscription) (*models.Subscription, error) {
	u := repository.ProviderSubscriptionUpdate{
		Provider: "stripe", ProviderSubscriptionID: sub.ID, ProviderCustomerID: nonEmpty(sub.Customer),
		ProviderStatus: sub.Status, CancelAtPeriodEnd: sub.CancelAtPeriodEnd, CurrentPeriodEnd: sub.PeriodEnd(),
		EndedAt: unixTime(sub.EndedAt),
	}
	if id, err := uuid.Parse(sub.Metadata["membership_subscription_id"]); err == nil {
		u.LocalID = &id
	}
	return s.repo.ApplyProviderSubscription(ctx, u)
}

func ignoreUnknownSubscription(err error) error {
	if errors.Is(err, repository.ErrSubscriptionNotFound) {
		return nil
	}
	return err
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ─── Wallet and entries ──────────────────────────────────────────────────

// Wallet is GET /customers/me/competition-points.
func (s *CompetitionPointsService) Wallet(ctx context.Context, customerID uuid.UUID) (*models.CPWallet, error) {
	return s.repo.CPWallet(ctx, customerID, 50)
}

// signQuote makes the opaque reference of a quote: what was quoted, to whom
// and until when, signed so it cannot be altered.
func (s *CompetitionPointsService) signQuote(customerID, competitionID uuid.UUID, version int, cost int64, expires time.Time) string {
	payload := fmt.Sprintf("%s|%s|%d|%d|%d", customerID, competitionID, version, cost, expires.Unix())
	mac := hmac.New(sha256.New, s.quoteSecret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type quoteRef struct {
	CustomerID, CompetitionID uuid.UUID
	Version                   int
	Cost                      int64
	Expires                   time.Time
}

func (s *CompetitionPointsService) readQuote(ref string) (*quoteRef, bool) {
	body, sig, ok := strings.Cut(ref, ".")
	if !ok {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, false
	}
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, s.quoteSecret)
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), want) {
		return nil, false
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 5 {
		return nil, false
	}
	var q quoteRef
	var errs [5]error
	q.CustomerID, errs[0] = uuid.Parse(parts[0])
	q.CompetitionID, errs[1] = uuid.Parse(parts[1])
	q.Version, errs[2] = strconv.Atoi(parts[2])
	q.Cost, errs[3] = strconv.ParseInt(parts[3], 10, 64)
	var unix int64
	unix, errs[4] = strconv.ParseInt(parts[4], 10, 64)
	for _, e := range errs {
		if e != nil {
			return nil, false
		}
	}
	q.Expires = time.Unix(unix, 0)
	return &q, true
}

// entryContext is what quoting and reserving both check.
func (s *CompetitionPointsService) entryContext(ctx context.Context, customerID uuid.UUID, channel string, competitionID uuid.UUID) (*repository.RoundForEntry, models.Decision, []string, error) {
	round, err := s.repo.RoundForEntry(ctx, competitionID)
	if errors.Is(err, repository.ErrCompetitionNotFound) || (err == nil && round.Status == "draft") {
		return nil, models.Decision{}, nil, ErrCompetitionNotFound
	}
	if err != nil {
		return nil, models.Decision{}, nil, err
	}
	if round.PointsUnit != "competition" {
		return nil, models.Decision{}, nil, cpRefuse(http.StatusConflict, models.ReasonCompetitionPointsDisabled,
			"This competition does not use competition points.", "view_competition")
	}
	ev, err := s.repo.Evidence(ctx, customerID)
	if err != nil {
		return nil, models.Decision{}, nil, err
	}
	d, approvals, err := s.decide(ctx, ev, models.OpCompetitionEntry, channel, nil)
	if err != nil {
		return nil, models.Decision{}, nil, err
	}
	if d.Allowed && !slices.Contains(round.CountryIDs, ev.CountryID) {
		d.Allowed, d.ReasonCode, d.NextAction = false, models.ReasonLocationUnavailable, "view_competitions"
		d.Message = "This competition isn't open to entrants in " + ev.CountryName + "."
	}
	sources := intersectSources(round.AllowedSources, approvedSources(approvals))
	return round, d, sources, nil
}

// Quote is what an entry would cost, against what the customer can spend on
// it. A quote reserves nothing.
func (s *CompetitionPointsService) Quote(ctx context.Context, customerID uuid.UUID, channel string, competitionID uuid.UUID) (*models.EntryQuote, error) {
	round, d, sources, err := s.entryContext(ctx, customerID, channel, competitionID)
	if err != nil {
		return nil, err
	}
	total, eligible, err := s.repo.Balances(ctx, customerID, sources)
	if err != nil {
		return nil, err
	}
	expires := s.now().Add(s.quoteTTL)
	q := &models.EntryQuote{
		CompetitionID: round.ID, Title: round.Title, RulesVersion: round.RulesVersion, Cost: round.Cost,
		EligibleBalance: eligible, TotalBalance: total, IneligibleBalance: total - eligible,
		AllowedSources: sources, CurrentPrizeCents: round.CurrentPrize, MaxPrizeCents: round.MaxPrize,
		Currency: round.Currency, EndsAt: round.EndsAt, ExpiresAt: expires,
	}
	switch {
	case !d.Allowed:
		q.ReasonCode, q.Message = d.ReasonCode, d.Message
	case !round.Open():
		q.ReasonCode, q.Message = models.ReasonCompetitionNotOpen, "This competition isn't taking entries right now."
	case len(sources) == 0:
		q.ReasonCode, q.Message = models.ReasonInsufficientEligible, "None of your points can be used for this competition."
	case eligible < round.Cost:
		q.ReasonCode = models.ReasonInsufficientEligible
		q.Message = fmt.Sprintf("You need %d eligible points; you have %d.", round.Cost, eligible)
	default:
		q.CanEnter = true
		q.Reference = s.signQuote(customerID, round.ID, round.RulesVersion, round.Cost, expires)
	}
	return q, nil
}

// ReserveRequest is the customer confirming a quote.
type ReserveRequest struct {
	Reference       string `json:"reference"`
	ClientRequestID string `json:"-"`
}

// Reserve holds the points for one entry after re-checking everything the
// quote showed.
func (s *CompetitionPointsService) Reserve(ctx context.Context, customerID uuid.UUID, channel string, competitionID uuid.UUID, req ReserveRequest) (*models.CPEntry, error) {
	key := strings.TrimSpace(req.ClientRequestID)
	if len(key) < 8 || len(key) > 120 {
		return nil, cpRefuse(http.StatusBadRequest, models.ReasonIdempotencyKeyConflict,
			"An Idempotency-Key of 8 to 120 characters is required.", "retry")
	}
	q, ok := s.readQuote(strings.TrimSpace(req.Reference))
	if !ok || q.CustomerID != customerID || q.CompetitionID != competitionID {
		return nil, cpRefuse(http.StatusBadRequest, models.ReasonQuoteExpired,
			"That entry quote isn't valid. Review the entry again.", "requote")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", competitionID, q.Version, q.Cost)))
	hash := hex.EncodeToString(sum[:])
	in := repository.ReserveInput{
		CompetitionID: competitionID, CustomerID: customerID, Channel: channel, RulesVersion: q.Version,
		Cost: q.Cost, ClientRequestID: key, RequestHash: hash, HoldTTL: s.holdTTL,
	}
	// A retry of a reservation already made returns it, even after the quote
	// lapsed.
	prior, err := s.repo.PriorEntry(ctx, customerID, key, hash)
	if errors.Is(err, repository.ErrKeyReused) {
		return nil, cpRefuse(http.StatusConflict, models.ReasonIdempotencyKeyConflict,
			"This request key was already used for a different entry.", "retry")
	}
	if err != nil {
		return nil, err
	}
	if prior != nil {
		return prior, nil
	}
	if s.now().After(q.Expires) {
		return nil, cpRefuse(http.StatusConflict, models.ReasonQuoteExpired,
			"That quote expired. Review the entry again.", "requote")
	}
	round, d, sources, err := s.entryContext(ctx, customerID, channel, competitionID)
	if err != nil {
		return nil, err
	}
	if !d.Allowed {
		return nil, refusalFrom(d)
	}
	if round.RulesVersion != q.Version || round.Cost != q.Cost {
		return nil, cpRefuse(http.StatusConflict, models.ReasonQuoteExpired,
			"The competition changed since you reviewed it. Review the entry again.", "requote")
	}
	in.Sources = sources
	entry, err := s.repo.ReserveEntry(ctx, in)
	var limit *repository.EntryLimitError
	switch {
	case err == nil:
		return entry, nil
	case errors.Is(err, repository.ErrKeyReused):
		return nil, cpRefuse(http.StatusConflict, models.ReasonIdempotencyKeyConflict,
			"This request key was already used for a different entry.", "retry")
	case errors.Is(err, repository.ErrCPInsufficient):
		return nil, cpRefuse(http.StatusConflict, models.ReasonInsufficientEligible,
			"You don't have enough eligible points for this entry.", "view_earning_rules")
	case errors.Is(err, repository.ErrEntryStale):
		return nil, cpRefuse(http.StatusConflict, models.ReasonQuoteExpired,
			"The competition changed since you reviewed it. Review the entry again.", "requote")
	case errors.Is(err, repository.ErrRoundNotLive):
		return nil, cpRefuse(http.StatusConflict, models.ReasonCompetitionNotOpen,
			"This competition isn't taking entries right now.", "view_competitions")
	case errors.As(err, &limit):
		return nil, cpRefuse(http.StatusConflict, models.ReasonEntryLimitReached,
			fmt.Sprintf("You've reached this competition's limit of %d entries.", limit.Limit), "view_competitions")
	}
	return nil, err
}

// StartEntry starts the attempt a reserved entry pays for. The capture and
// the attempt commit together. If the start is refused before it began, the
// hold is released at once; an outcome we cannot confirm keeps the hold
// for the reconciliation.
func (s *CompetitionPointsService) StartEntry(ctx context.Context, customerID uuid.UUID, competitionID, entryID uuid.UUID, clientRequestID string, risk map[string]any) (*models.AttemptStartView, error) {
	entry, err := s.repo.GetEntry(ctx, customerID, entryID)
	if errors.Is(err, repository.ErrEntryNotFound) || (err == nil && entry.CompetitionID != competitionID) {
		return nil, cpRefuse(http.StatusNotFound, "ENTRY_NOT_FOUND", "That entry wasn't found.", "view_points")
	}
	if err != nil {
		return nil, err
	}
	ev, err := s.repo.Evidence(ctx, customerID)
	if err != nil {
		return nil, err
	}
	stopped, err := s.repo.StopActive(ctx, models.StopCompetitionStarts, ev.CountryID)
	if err != nil {
		return nil, err
	}
	if stopped && entry.State == models.EntryReserved {
		_ = s.repo.ReleaseEntry(ctx, entryID, "starts are paused", s.restoreWindow)
		return nil, cpRefuse(http.StatusForbidden, models.ReasonOperationStopped,
			"Competition starts are paused. Your points have been returned.", "view_points")
	}
	if strings.TrimSpace(clientRequestID) == "" {
		clientRequestID = "entry:" + entryID.String()
	}
	view, err := s.competitions.Play(ctx, competitionID, SocialActor{CustomerID: customerID.String()}, PlayRequest{
		ClientRequestID: clientRequestID, RiskMetadata: risk, EntryID: &entryID,
	})
	var refusal *PlayRefusal
	if errors.As(err, &refusal) {
		// Refused inside the play transaction, which rolled back: the start
		// did not happen, so the hold can be released. A conflicting key is
		// a different request and leaves the entry alone.
		if refusal.Code != PlayIdempotencyConflict && refusal.Code != PlayBusy {
			if rerr := s.repo.ReleaseEntry(ctx, entryID, "the attempt could not start: "+refusal.Code, s.restoreWindow); rerr != nil {
				log.Printf("competition points: release entry %s after refusal: %v", entryID, rerr)
			}
		}
	}
	return view, err
}

// CancelEntry releases an entry the customer no longer wants, before it
// starts.
func (s *CompetitionPointsService) CancelEntry(ctx context.Context, customerID, entryID uuid.UUID) (*models.CPEntry, error) {
	e, err := s.repo.CancelEntry(ctx, customerID, entryID, s.restoreWindow)
	switch {
	case errors.Is(err, repository.ErrEntryNotFound):
		return nil, cpRefuse(http.StatusNotFound, "ENTRY_NOT_FOUND", "That entry wasn't found.", "view_points")
	case errors.Is(err, repository.ErrEntryNotHeld):
		return nil, cpRefuse(http.StatusConflict, models.ReasonStartUncertain,
			"This entry has already started or is being confirmed.", "check_status")
	}
	return e, err
}

// ─── The job ─────────────────────────────────────────────────────────────

// RunOnce is one pass of the competition-points job.
func (s *CompetitionPointsService) RunOnce(ctx context.Context) (*models.CPRunResult, error) {
	res, err := s.repo.RunCompetitionPoints(ctx, 200, s.EarningGate, s.restoreWindow)
	if err != nil {
		return res, err
	}
	if err := s.syncStripeMemberships(ctx); err != nil {
		log.Printf("competition points: membership sync: %v", err)
	}
	if err := s.enforceRenewals(ctx); err != nil {
		log.Printf("competition points: renewal check: %v", err)
	}
	return res, nil
}

// enforceRenewals re-checks each renewing membership: where renewal is no
// longer approved, or is stopped, it is set to end at the period already
// paid for. Benefits of that period are kept.
func (s *CompetitionPointsService) enforceRenewals(ctx context.Context) error {
	subs, err := s.repo.RenewingSubscriptions(ctx, 200)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, sub := range subs {
		key := sub.CountryID.String() + "|" + sub.Provider
		ok, seen := allowed[key]
		if !seen {
			stopped, err := s.repo.StopActive(ctx, models.StopMembershipRenewals, sub.CountryID)
			if err != nil {
				return err
			}
			provider := sub.Provider
			approvals, err := s.repo.CurrentApprovals(ctx, repository.ApprovalQuery{
				Operation: models.OpMembershipRenewal, CountryID: sub.CountryID, Channel: "any", Provider: &provider,
			})
			if err != nil {
				return err
			}
			ok = !stopped && len(approvals) > 0
			allowed[key] = ok
		}
		if ok {
			continue
		}
		log.Printf("competition points: renewal not approved for membership %s; ending it at period end", sub.ID)
		switch sub.Provider {
		case "stripe":
			if s.stripe == nil {
				continue
			}
			stripeSub, err := s.stripe.CancelSubscriptionAtPeriodEnd(ctx, sub.ProviderSubscriptionID,
				"membership-renewal-stop-"+sub.ID.String())
			if err != nil {
				log.Printf("competition points: could not stop renewal of %s: %v", sub.ID, err)
				continue
			}
			if _, err := s.applyStripeSubscription(ctx, stripeSub); err != nil {
				return err
			}
		default:
			if _, err := s.repo.ApplyProviderSubscription(ctx, repository.ProviderSubscriptionUpdate{
				Provider: sub.Provider, ProviderSubscriptionID: sub.ProviderSubscriptionID, LocalID: &sub.ID,
				ProviderStatus: "active", CancelAtPeriodEnd: true,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// RunLoop runs the job on an interval until ctx ends, on one instance at a
// time.
func (s *CompetitionPointsService) RunLoop(ctx context.Context, every time.Duration, exclusive Exclusive) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		_, err := exclusive(ctx, func(ctx context.Context) error {
			res, err := s.RunOnce(ctx)
			if err != nil {
				return err
			}
			if res.GiftsAwarded+res.BonusesAwarded+res.GiftsReversed+res.LotsExpired+res.EntriesReleased > 0 {
				log.Printf("competition points: %+v", *res)
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			log.Printf("competition points job: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// MemberDiscountView is the product discount a customer gets right now.
type MemberDiscountView struct {
	// DiscountBPS is the percentage off the gift price, in basis points,
	// already capped by the platform commission.
	DiscountBPS int `json:"discount_bps"`
	// PeriodEnd is when the paid period giving it ends.
	PeriodEnd *time.Time `json:"period_end,omitempty"`
}

// MemberDiscount is GET /customers/me/member-discount, for showing the
// saving in the bag before an order is placed. The order itself is priced on
// the server, and this is only what it will do.
func (s *CompetitionPointsService) MemberDiscount(ctx context.Context, customerID uuid.UUID) (*MemberDiscountView, error) {
	bps, period, err := s.repo.MemberDiscount(ctx, customerID)
	if err != nil {
		return nil, err
	}
	out := &MemberDiscountView{}
	if bps <= 0 || s.platformFeeBPS <= 0 {
		return out, nil
	}
	out.DiscountBPS = min(bps, s.platformFeeBPS)
	if end, err := s.repo.PeriodEnd(ctx, period); err == nil {
		out.PeriodEnd = end
	}
	return out, nil
}
