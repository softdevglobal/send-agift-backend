package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/games"
	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrCompetitionNotFound = errors.New("competition not found")
	ErrCompetitionNotLive  = errors.New("competition is not open yet")
	ErrCompetitionClosed   = errors.New("competition has closed")
	ErrCompetitionLocked   = errors.New("competition rules are locked once it has started")
	ErrSuperAdminOnly      = errors.New("only a super admin can change a published competition")
	ErrCompetitionState    = errors.New("competition is not in the right state for this action")
	ErrInvalidCompetition  = errors.New("invalid competition")
	ErrScheduleBlocked     = errors.New("competition cannot be scheduled")
	ErrNotEligible         = errors.New("not eligible to enter")
	ErrAttemptLimit        = errors.New("no attempts left in this competition")
	ErrAttemptBusy         = errors.New("another attempt is starting, try again")
	ErrCustomerRequired    = errors.New("sign in to enter competitions")
	ErrTieAtCutoff         = errors.New("a tie at the prize cutoff needs a skill playoff")
	ErrUnresolvedScores    = errors.New("scores are still awaiting review")
	ErrWinnerNotFound      = errors.New("winner not found")
	ErrWinnerState         = errors.New("winner is not in the right state for this action")
	ErrClaimNotFound       = errors.New("prize claim not found")
	ErrClaimState          = errors.New("prize claim is not in the right state for this action")
	ErrInvalidClaim        = errors.New("invalid prize claim")
	ErrInvalidReview       = errors.New("invalid review")
	ErrReserveLocked       = errors.New("prize reserve can no longer change")
	ErrInvalidReserve      = errors.New("invalid prize reserve")
	ErrConfigConflict      = errors.New("this round was changed by someone else; reload it and try again")
	ErrInvalidAdjustment   = errors.New("invalid prize adjustment")
	ErrPrizeOutOfRange     = errors.New("that would take the prize below zero or above its maximum")
	ErrPlayNotFound        = errors.New("play not found")
	ErrPlayState           = errors.New("play is not in the right state for this action")
	ErrWinnerSettled       = errors.New("this winner has already been paid")
)

const (
	// Winners have 14 days from validation to claim (§19.2).
	prizeClaimWindow = 14 * 24 * time.Hour
	// How far below the prize places finalisation looks, to cover players
	// who turn out to be ineligible.
	finaliseWindow = 50
	// Enough of the board to find a replacement winner.
	replacementWindow = 500
	// A snapshot holds the whole board.
	snapshotLimit = 10000
)

// Cancellation reasons permitted by §13.9. Anything else is not a reason to
// stop a live competition.
var cancelReasons = map[string]bool{
	"technical_failure":           true,
	"security_breach":             true,
	"legal_direction":             true,
	"provider_or_store_direction": true,
	"platform_outage":             true,
	"prize_unavailable":           true,
	"fairness_failure":            true,
}

// Machine codes for a refused play (Progressive Prize spec §5.3). The
// handler maps each to its HTTP status; every one means nothing was charged.
const (
	PlayGameNotActive       = "GAME_NOT_ACTIVE"
	PlayOutsideWindow       = "OUTSIDE_GAME_WINDOW"
	PlayLimitReached        = "PLAY_LIMIT_REACHED"
	PlayIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	PlayInsufficientPoints  = "INSUFFICIENT_POINTS"
	PlayPrizeCapReached     = "PRIZE_CAP_REACHED"
	PlayNotEligible         = "NOT_ELIGIBLE"
	PlayBusy                = "PLAY_BUSY"
	PlayTransactionFailed   = "PLAY_TRANSACTION_FAILED"
	// PlaySignInRequired is a guest trying to start a game that costs points.
	PlaySignInRequired = "SIGN_IN_REQUIRED"
)

// PlayRefusal is a play the server turned away before anything was charged,
// with the reason's machine code and whatever the player needs to act on it
// (points required, when the limit resets).
type PlayRefusal struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *PlayRefusal) Error() string { return e.Message }

func refuse(code, message string, details map[string]any) *PlayRefusal {
	return &PlayRefusal{Code: code, Message: message, Details: details}
}

// AdminActor is who performed an admin action, for the audit log.
type AdminActor struct {
	ID        uuid.UUID
	IP        string
	UserAgent string
	// SuperAdmin may change a published competition; other admins only
	// set up drafts.
	SuperAdmin bool
}

func (a AdminActor) ledgerActor() repository.Actor {
	id := a.ID
	return repository.Actor{Type: "admin", ID: &id}
}

func (a AdminActor) audit(action string, entityID uuid.UUID, reason *string) models.AuditEntry {
	id := a.ID
	entry := models.AuditEntry{
		ActorType:  "admin",
		ActorID:    &id,
		Action:     action,
		EntityType: "competition",
		EntityID:   &entityID,
		Reason:     reason,
	}
	if a.IP != "" {
		entry.IPAddress = &a.IP
	}
	if a.UserAgent != "" {
		entry.UserAgent = &a.UserAgent
	}
	return entry
}

// CompetitionService runs skill competitions end to end: publishing them,
// official attempts, the live leaderboard, freezing, finalising winners and
// prize claims. Every rule it enforces comes from the Master Plan §13–§20.
type CompetitionService struct {
	repo         *repository.CompetitionRepository
	games        *repository.GameRepository
	customers    *repository.CustomerRepository
	capabilities *repository.CountryCapabilityRepository
	countries    *repository.CountryRepository
	points       *repository.PointsRepository
	now          func() time.Time
}

func NewCompetitionService(
	repo *repository.CompetitionRepository,
	gameRepo *repository.GameRepository,
	customers *repository.CustomerRepository,
	capabilities *repository.CountryCapabilityRepository,
	countries *repository.CountryRepository,
	points *repository.PointsRepository,
) *CompetitionService {
	return &CompetitionService{
		repo:         repo,
		games:        gameRepo,
		customers:    customers,
		capabilities: capabilities,
		countries:    countries,
		points:       points,
		now:          time.Now,
	}
}

// ─── Loading ──────────────────────────────────────────────────────────────

// load brings statuses up to the clock, then reads one competition.
func (s *CompetitionService) load(ctx context.Context, id uuid.UUID) (*models.Competition, error) {
	if err := s.repo.SyncStatuses(ctx); err != nil {
		return nil, err
	}
	c, err := s.repo.GetByID(ctx, id)
	if errors.Is(err, repository.ErrCompetitionNotFound) {
		return nil, ErrCompetitionNotFound
	}
	return c, err
}

// status is the competition's state right now, to the second, even if the
// stored row has not been synced yet.
func (s *CompetitionService) status(c *models.Competition) string {
	return effectiveCompetitionStatus(c.Status, c.StartsAt, c.EndsAt, s.now())
}

func parseCustomer(actor SocialActor) (*uuid.UUID, error) {
	if actor.CustomerID == "" {
		return nil, nil
	}
	id, err := uuid.Parse(actor.CustomerID)
	if err != nil {
		return nil, ErrCustomerRequired
	}
	return &id, nil
}

// ─── Eligibility ──────────────────────────────────────────────────────────

// capabilityCache avoids reading the same country's capabilities repeatedly
// within one request. A nil entry means the country has none set.
type capabilityCache map[uuid.UUID]*models.CountryCapability

func (s *CompetitionService) capability(ctx context.Context, countryID uuid.UUID, cache capabilityCache) (*models.CountryCapability, error) {
	if cc, ok := cache[countryID]; ok {
		return cc, nil
	}
	cc, err := s.capabilities.GetByCountryID(ctx, countryID.String())
	if errors.Is(err, repository.ErrCountryCapabilityNotFound) {
		cache[countryID] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cache[countryID] = cc
	return cc, nil
}

// eligibility checks a customer against a competition's published rules:
// one of its countries, that country's skill-competition gate, 18+ with verified age, and identity
// verification when the rules require it (§13.5). The returned reason is
// written for the player.
func (s *CompetitionService) eligibility(ctx context.Context, c *models.Competition, cust *models.Customer, cache capabilityCache) (bool, string, error) {
	if cust.Status != "active" {
		return false, "Your account is not active.", nil
	}
	if !c.HasCountry(cust.CountryID) {
		return false, fmt.Sprintf("This competition is only open to players in %s.", c.CountryNames()), nil
	}
	// Each country's own gates apply to its players.
	cc, err := s.capability(ctx, cust.CountryID, cache)
	if err != nil {
		return false, "", err
	}
	if cc == nil || !cc.SkillCompetitionsEnabled {
		return false, "Skill competitions are not available in your country yet.", nil
	}
	if c.PrizeGrowthEnabled && !cc.ProgressivePrizesEnabled {
		return false, "Growing-prize rounds are not available in your country yet.", nil
	}
	if c.GameType == "chance" && !cc.ChanceGamesEnabled {
		return false, "Prize games of chance are not available in your country.", nil
	}
	if c.PointsPerAttempt > 0 && !cc.PointsUsageEnabled {
		return false, "Points cannot be spent in your country yet.", nil
	}
	if cust.DateOfBirth == nil {
		return false, "Add your date of birth to your profile to enter.", nil
	}
	if ageOn(*cust.DateOfBirth, s.now()) < c.MinAge {
		return false, fmt.Sprintf("You must be %d or older to enter.", c.MinAge), nil
	}
	if cust.AgeVerifiedAt == nil {
		return false, "Verify your age to enter.", nil
	}
	if c.RequiresIdentityVerification && cust.IdentityVerifiedAt == nil {
		return false, "Verify your identity to enter.", nil
	}
	return true, "", nil
}

func (s *CompetitionService) customer(ctx context.Context, id uuid.UUID) (*models.Customer, error) {
	cust, err := s.customers.GetByID(ctx, id.String())
	if errors.Is(err, repository.ErrCustomerNotFound) {
		return nil, ErrCustomerRequired
	}
	return cust, err
}

// ─── Customer views ───────────────────────────────────────────────────────

func (s *CompetitionService) toView(c *models.Competition) models.CompetitionView {
	return models.CompetitionView{
		ID:                           c.ID,
		Title:                        c.Title,
		Status:                       s.status(c),
		GameSlug:                     c.GameSlug,
		GameName:                     c.GameName,
		GameType:                     c.GameType,
		WinnerMethod:                 c.WinnerMethod,
		WinOdds:                      c.WinOdds,
		Countries:                    c.Countries,
		CountryCode:                  c.CountryCodes(),
		CountryName:                  c.CountryNames(),
		StartsAt:                     c.StartsAt,
		EndsAt:                       c.EndsAt,
		Timezone:                     c.Timezone,
		PointsPerAttempt:             c.PointsPerAttempt,
		PointsDeductionEnabled:       true,
		MaxAttemptsPerCustomer:       c.MaxAttemptsPerCustomer,
		MinAge:                       c.MinAge,
		RequiresIdentityVerification: c.RequiresIdentityVerification,
		NumberOfWinners:              c.NumberOfWinners,
		PrizeDescription:             c.PrizeDescription,
		PrizeValueAmount:             c.PrizeValueAmount,
		PrizeCurrency:                c.PrizeCurrency,
		OfficialRules:                c.OfficialRules,
		CancelReason:                 c.CancelReason,
		CancelNote:                   c.CancelNote,
		PrizeGrowthEnabled:           c.PrizeGrowthEnabled,
		PrizeType:                    c.PrizeType,
		PrizePoints:                  c.PrizePoints,
		StartPrizeCents:              c.StartPrizeCents,
		CurrentPrizeCents:            c.CurrentPrizeCents,
		IncrementPerPlayCents:        c.IncrementPerPlayCents,
		MaxPrizeCents:                c.MaxPrizeCents,
		PrizeCapReached:              capReached(c),
		ContinueAtCap:                c.ContinueAtCap,
		FinalPrizeCents:              c.FinalPrizeCents,
		EligiblePlayCount:            c.EligiblePlayCount,
		UniquePlayerCount:            c.UniquePlayerCount,
		DailyPlayLimit:               c.DailyPlayLimit,
		PrizeVersion:                 c.PrizeVersion,
		RoundNo:                      c.RoundNo,
	}
}

func (s *CompetitionService) me(ctx context.Context, c *models.Competition, cust *models.Customer, stat repository.MyStat, cache capabilityCache) (*models.CompetitionMe, error) {
	eligible, reason, err := s.eligibility(ctx, c, cust, cache)
	if err != nil {
		return nil, err
	}
	remaining := c.MaxAttemptsPerCustomer - stat.AttemptsUsed
	if remaining < 0 {
		remaining = 0
	}
	balance, err := s.points.Balance(ctx, cust.ID)
	if err != nil {
		return nil, err
	}
	me := &models.CompetitionMe{
		AttemptsUsed:      stat.AttemptsUsed,
		AttemptsRemaining: remaining,
		BestScore:         stat.BestScore,
		Eligible:          eligible,
		IneligibleReason:  reason,
		PointsBalance:     balance,
	}
	if c.DailyPlayLimit != nil {
		start, next := repository.DayWindow(s.now(), c.Timezone)
		today, err := s.repo.DailyPlays(ctx, c.ID, cust.ID, start)
		if err != nil {
			return nil, err
		}
		left := *c.DailyPlayLimit - today
		if left < 0 {
			left = 0
		}
		me.PlaysLeftToday = &left
		if next.Before(c.EndsAt) {
			me.DailyResetAt = &next
		}
	}
	return me, nil
}

// ListCompetitions returns competitions for the app. Signed-in customers see
// the competitions open in their country with their attempts and eligibility;
// guests see everything that has been published.
func (s *CompetitionService) ListCompetitions(ctx context.Context, actor SocialActor) ([]models.CompetitionView, error) {
	if err := s.repo.SyncStatuses(ctx); err != nil {
		return nil, err
	}
	customerID, err := parseCustomer(actor)
	if err != nil {
		return nil, err
	}

	var cust *models.Customer
	filter := repository.CompetitionFilter{}
	if customerID != nil {
		if cust, err = s.customer(ctx, *customerID); err != nil {
			return nil, err
		}
		filter.CountryID = &cust.CountryID
	}

	list, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	out := make([]models.CompetitionView, 0, len(list))
	var stats map[uuid.UUID]repository.MyStat
	if cust != nil {
		ids := make([]uuid.UUID, len(list))
		for i, c := range list {
			ids[i] = c.ID
		}
		if stats, err = s.repo.MyStats(ctx, cust.ID, ids); err != nil {
			return nil, err
		}
	}
	cache := capabilityCache{}
	for i := range list {
		c := &list[i]
		view := s.toView(c)
		view.OfficialRules = nil // the list stays light; rules come with the detail
		if cust != nil {
			if view.Me, err = s.me(ctx, c, cust, stats[c.ID], cache); err != nil {
				return nil, err
			}
		}
		out = append(out, view)
	}
	return out, nil
}

// GetCompetition returns one competition with its rules and disclosures, the
// caller's position when signed in, and published winners once finalised.
func (s *CompetitionService) GetCompetition(ctx context.Context, id uuid.UUID, actor SocialActor) (*models.CompetitionView, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status == "draft" {
		return nil, ErrCompetitionNotFound
	}
	view := s.toView(c)

	customerID, err := parseCustomer(actor)
	if err != nil {
		return nil, err
	}
	if customerID != nil {
		cust, err := s.customer(ctx, *customerID)
		if err != nil {
			return nil, err
		}
		stats, err := s.repo.MyStats(ctx, cust.ID, []uuid.UUID{c.ID})
		if err != nil {
			return nil, err
		}
		if view.Me, err = s.me(ctx, c, cust, stats[c.ID], capabilityCache{}); err != nil {
			return nil, err
		}
		s.repo.RecordView(ctx, c.ID, cust.ID)
		_, mine, _, err := s.repo.CompetitionLeaderboard(ctx, c.ID, 0, customerID)
		if err != nil {
			return nil, err
		}
		if mine != nil {
			rank := mine.Rank
			view.Me.Rank = &rank
		}
	}

	if c.Status == "finalised" {
		winners, err := s.repo.Winners(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		for _, w := range winners {
			// Only validated winners are published (§19.1).
			if w.Status == "validated" {
				view.Winners = append(view.Winners, models.PublicWinner{
					PrizePosition: w.PrizePosition,
					Rank:          w.Rank,
					DisplayName:   publicDisplayName(w.DisplayName),
					CountryName:   w.CountryName,
					Score:         w.Score,
					AchievedAt:    w.AchievedAt,
				})
			}
			if customerID != nil && w.CustomerID == *customerID && view.Me != nil {
				win := &models.MyWin{WinnerID: w.ID, PrizePosition: w.PrizePosition, Status: w.Status}
				if w.Claim != nil {
					win.ClaimID = &w.Claim.ID
					win.ClaimStatus = &w.Claim.Status
					win.ClaimDeadlineAt = &w.Claim.ClaimDeadlineAt
				}
				view.Me.Win = win
			}
		}
	}
	return &view, nil
}

// Leaderboard returns a competition's live board: rank, shortened name,
// country, score, time and status, plus the caller's own row even when they
// rank outside the top.
func (s *CompetitionService) Leaderboard(ctx context.Context, id uuid.UUID, actor SocialActor, limit int) (*models.CompetitionLeaderboardView, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status == "draft" {
		return nil, ErrCompetitionNotFound
	}
	customerID, err := parseCustomer(actor)
	if err != nil {
		return nil, err
	}
	limit = clampLimit(limit)

	top, mine, total, err := s.repo.CompetitionLeaderboard(ctx, c.ID, limit, customerID)
	if err != nil {
		return nil, err
	}
	final := c.Status == "finalised"
	view := &models.CompetitionLeaderboardView{
		CompetitionID: c.ID,
		Status:        s.status(c),
		Final:         final,
		Entries:       make([]models.LeaderboardEntry, 0, len(top)),
		TotalPlayers:  total,
	}
	isMe := func(row repository.LeaderRow) bool {
		return customerID != nil && row.CustomerID != nil && *row.CustomerID == *customerID
	}
	for _, row := range top {
		view.Entries = append(view.Entries, publicEntry(row, leaderboardStatus(row.ValidationStatus, final), isMe(row)))
	}
	if mine != nil {
		entry := publicEntry(*mine, leaderboardStatus(mine.ValidationStatus, final), true)
		view.Me = &entry
	}
	return view, nil
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLeaderboardLimit
	case limit > maxLeaderboardLimit:
		return maxLeaderboardLimit
	}
	return limit
}

// publicEntry shapes a ranked row for the public: no ids, shortened name.
func publicEntry(row repository.LeaderRow, status string, isMe bool) models.LeaderboardEntry {
	e := models.LeaderboardEntry{
		Rank:        row.Rank,
		DisplayName: "Player",
		Score:       row.Score,
		DurationMs:  row.DurationMs,
		AchievedAt:  row.AchievedAt,
		Status:      status,
		IsMe:        isMe,
	}
	if row.CustomerID != nil {
		e.DisplayName = publicDisplayName(row.DisplayName)
	}
	if row.CountryCode != nil {
		e.CountryCode = *row.CountryCode
	}
	if row.CountryName != nil {
		e.CountryName = *row.CountryName
	}
	return e
}

// ─── Official attempts ────────────────────────────────────────────────────

// PlayRequest is one intended play. ClientRequestID is the idempotency key:
// the same key from the same customer always means the same play (spec §4.2).
type PlayRequest struct {
	ClientRequestID string
	RiskMetadata    map[string]any
}

const maxClientRequestID = 128

// Play is the spec's POST /games/{id}/plays: it checks the customer can
// enter, then runs the atomic play transaction — points debit, the play, the
// prize increment and the cached prize, together or not at all. Every entrant
// gets the round's single seed, so everyone plays the identical game (§14.1).
//
// A refusal is a *PlayRefusal carrying the spec's machine code; nothing is
// charged for any refusal.
func (s *CompetitionService) Play(ctx context.Context, id uuid.UUID, actor SocialActor, req PlayRequest) (*models.AttemptStartView, error) {
	customerID, err := parseCustomer(actor)
	if err != nil {
		return nil, err
	}
	if customerID == nil {
		return nil, ErrCustomerRequired
	}
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	if req.ClientRequestID == "" || len(req.ClientRequestID) > maxClientRequestID {
		return nil, refuse(PlayIdempotencyConflict,
			"an Idempotency-Key of 1 to 128 characters is required for every play", nil)
	}

	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status == "draft" {
		return nil, ErrCompetitionNotFound
	}
	if _, ok := games.EngineFor(c.GameSlug); !ok {
		if _, chance := games.ChanceMechanic(c.GameSlug); !chance && c.GameSlug != games.QuizSlug {
			return nil, ErrGameNotFound
		}
	}
	cust, err := s.customer(ctx, *customerID)
	if err != nil {
		return nil, err
	}

	reject := func(r *PlayRefusal) (*models.AttemptStartView, error) {
		s.repo.RecordRejection(ctx, c.ID, customerID, r.Code)
		return nil, r
	}
	eligible, reason, err := s.eligibility(ctx, c, cust, capabilityCache{})
	if err != nil {
		return nil, err
	}
	if !eligible {
		return reject(refuse(PlayNotEligible, reason, nil))
	}

	outcome, err := s.repo.StartPlay(ctx, repository.PlayInput{
		CompetitionID:   c.ID,
		CustomerID:      cust.ID,
		ClientRequestID: req.ClientRequestID,
		SessionTTL:      time.Duration(games.SessionTTLSeconds(c.GameConfig)) * time.Second,
		RiskMetadata:    req.RiskMetadata,
	})
	var limit *repository.PlayLimitError
	var broke *repository.InsufficientPointsError
	switch {
	case err == nil:
	case errors.Is(err, repository.ErrCompetitionNotFound):
		return nil, ErrCompetitionNotFound
	case errors.Is(err, repository.ErrRoundPaused):
		return reject(refuse(PlayGameNotActive, "This round is paused. Check back soon.", map[string]any{"status": "paused"}))
	case errors.Is(err, repository.ErrRoundNotStarted):
		return reject(refuse(PlayGameNotActive, "This round has not opened yet.",
			map[string]any{"status": "scheduled", "starts_at": c.StartsAt}))
	case errors.Is(err, repository.ErrRoundNotLive):
		return reject(refuse(PlayGameNotActive, "This round is no longer taking plays.",
			map[string]any{"status": s.status(c)}))
	case errors.Is(err, repository.ErrRoundOutsideWindow):
		return reject(refuse(PlayOutsideWindow, "This round has closed.", map[string]any{"ends_at": c.EndsAt}))
	case errors.As(err, &limit):
		details := map[string]any{"limit": limit.Limit, "kind": limit.Kind}
		msg := "You have used every play in this round."
		if limit.Kind == "daily" {
			msg = "You have used today's plays."
			if limit.NextEligibleAt != nil {
				details["next_eligible_at"] = *limit.NextEligibleAt
			}
		}
		return reject(refuse(PlayLimitReached, msg, details))
	case errors.As(err, &broke):
		return reject(refuse(PlayInsufficientPoints, "You do not have enough points to play.",
			map[string]any{"points_required": broke.Required, "points_balance": broke.Balance}))
	case errors.Is(err, repository.ErrPrizeCapReached):
		return reject(refuse(PlayPrizeCapReached,
			"The prize has reached its maximum and this round has stopped taking plays.", nil))
	case errors.Is(err, repository.ErrPlayKeyConflict):
		return reject(refuse(PlayIdempotencyConflict,
			"This Idempotency-Key was already used for a different play.", nil))
	case errors.Is(err, repository.ErrAttemptConflict):
		return reject(refuse(PlayBusy, "Another play is starting. Try again.", nil))
	default:
		return nil, err
	}

	a := outcome.Attempt
	remaining := c.MaxAttemptsPerCustomer - outcome.AttemptsUsed
	if remaining < 0 {
		remaining = 0
	}
	status := "COMPLETED"
	if a.Status == "voided" {
		status = "VOIDED"
		if a.RefundedAt != nil {
			status = "REFUNDED"
		}
	}
	view := &models.AttemptStartView{
		AttemptID:             a.ID,
		AttemptNumber:         a.AttemptNumber,
		AttemptsRemaining:     remaining,
		PointsSpent:           a.PointsSpent,
		PlayID:                a.ID,
		Status:                status,
		WalletPointsRemaining: outcome.PointsBalance,
		PrizeIncrementCents:   a.PrizeIncrementCents,
		PrizeCapReached:       outcome.CapReached,
		PlayedAt:              a.StartedAt,
		Replayed:              outcome.Replayed,
		Result:                a.ResultPayload,
		Session: models.GameSessionView{
			SessionID: outcome.Session.ID,
			GameSlug:  c.GameSlug,
			Version:   c.GameVersion,
			Mode:      outcome.Session.Mode,
			Seed:      outcome.Session.ServerSeed,
			Config:    outcome.Session.Config,
			StartedAt: outcome.Session.StartedAt,
			ExpiresAt: outcome.Session.ExpiresAt,
		},
	}
	if a.PrizeBeforeCents != nil {
		view.PrizeBeforeCents = *a.PrizeBeforeCents
	}
	if a.PrizeAfterCents != nil {
		view.PrizeAfterCents = *a.PrizeAfterCents
	}
	return view, nil
}

// StartAttempt is the older POST /competitions/{id}/attempts. Builds of the
// app that predate idempotency keys get a fresh key per request, which is
// what they always had: no retry protection, but no double charge either,
// because each request is a distinct intended play.
func (s *CompetitionService) StartAttempt(ctx context.Context, id uuid.UUID, actor SocialActor, clientRequestID string) (*models.AttemptStartView, error) {
	if strings.TrimSpace(clientRequestID) == "" {
		clientRequestID = "legacy:" + uuid.NewString()
	}
	return s.Play(ctx, id, actor, PlayRequest{ClientRequestID: clientRequestID})
}

// SubmitOfficial scores an official attempt. GameService hands official
// sessions here after checking the session belongs to the submitter.
func (s *CompetitionService) SubmitOfficial(ctx context.Context, session *models.GameSession, in SubmitScoreInput) (*models.GameScoreView, error) {
	if session.CustomerID == nil || session.CompetitionID == nil {
		return nil, ErrGameSessionNotFound
	}
	customerID := *session.CustomerID

	attempt, err := s.repo.GetAttemptBySession(ctx, session.ID)
	if errors.Is(err, repository.ErrAttemptNotFound) {
		return nil, ErrGameSessionNotFound
	}
	if err != nil {
		return nil, err
	}

	// A retried request returns the score already recorded.
	if session.Status == "submitted" {
		existing, err := s.repo.GetSubmissionBySession(ctx, session.ID)
		if errors.Is(err, repository.ErrSubmissionNotFound) {
			return nil, ErrGameSessionNotFound
		}
		if err != nil {
			return nil, err
		}
		return s.officialView(ctx, existing, nil, false)
	}
	if session.Status != "active" || attempt.Status == "voided" {
		return nil, ErrGameSessionExpired
	}

	c, err := s.load(ctx, *session.CompetitionID)
	if err != nil {
		return nil, err
	}

	// The server clock decides: a score received at or after the close does
	// not count, however the game went (§13.6).
	// A play that was accepted before a pause may still be finished and
	// scored; the pause only stops new plays.
	now := s.now()
	if st := s.status(c); st != "live" && st != "paused" {
		if err := s.games.ExpireSession(ctx, session.ID); err != nil {
			return nil, err
		}
		return nil, ErrCompetitionClosed
	}
	if now.After(session.ExpiresAt) {
		if err := s.games.ExpireSession(ctx, session.ID); err != nil {
			return nil, err
		}
		return nil, ErrGameSessionExpired
	}

	scorer, err := s.scorer(ctx, c.ID, session.GameSlug)
	if err != nil {
		return nil, err
	}
	durationMs := now.Sub(session.StartedAt).Milliseconds()
	base := repository.OfficialScoreInput{
		SessionID:     session.ID,
		AttemptID:     attempt.ID,
		CompetitionID: c.ID,
		CustomerID:    customerID,
		ClientScore:   in.ClientScore,
		DurationMs:    durationMs,
		EventLog:      in.Moves,
		EventLogHash:  hashMoves(in.Moves),
	}

	result, replayErr := scorer(session.ServerSeed, session.Config, in.Moves)
	if replayErr != nil {
		reason := replayErr.Error()
		base.MovesCount = len(in.Moves)
		base.ValidationStatus = "rejected"
		base.ReviewReason = &reason
		if _, err := s.repo.SaveOfficialScore(ctx, base); err != nil && !errors.Is(err, repository.ErrGameSessionNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", ErrInvalidMoveLog, reason)
	}

	// The same anti-cheat signals as practice. A flagged score stays on the
	// board marked "under review" instead of disappearing.
	status := "accepted"
	var reviewReason *string
	if durationMs < result.MinDurationMs {
		reason := fmt.Sprintf("played %d moves in %dms, faster than the %dms floor",
			result.MovesUsed, durationMs, result.MinDurationMs)
		status, reviewReason = "manual_review", &reason
	} else if in.ClientScore != nil && *in.ClientScore != result.Score {
		reason := fmt.Sprintf("client reported %d, server replayed %d", *in.ClientScore, result.Score)
		status, reviewReason = "manual_review", &reason
	}

	base.Score = result.Score
	base.MovesCount = result.MovesUsed
	base.Stats = result.Stats
	base.ValidationStatus = status
	base.ReviewReason = reviewReason

	saved, err := s.repo.SaveOfficialScore(ctx, base)
	if errors.Is(err, repository.ErrGameSessionNotFound) {
		existing, getErr := s.repo.GetSubmissionBySession(ctx, session.ID)
		if getErr != nil {
			return nil, ErrGameSessionNotFound
		}
		return s.officialView(ctx, existing, nil, false)
	}
	if err != nil {
		return nil, err
	}
	return s.officialView(ctx, saved, result, true)
}

// scorer is how a round's plays are scored: a skill game's replay engine,
// or — for a quiz — the round's own questions, answers included, which never
// leave the server.
func (s *CompetitionService) scorer(ctx context.Context, competitionID uuid.UUID, slug string) (func(seed string, config json.RawMessage, moves []string) (*games.Result, error), error) {
	if slug == games.QuizSlug {
		questions, err := s.repo.QuizQuestions(ctx, competitionID)
		if err != nil {
			return nil, err
		}
		return func(_ string, _ json.RawMessage, moves []string) (*games.Result, error) {
			return games.ScoreQuiz(questions, moves)
		}, nil
	}
	engine, ok := games.EngineFor(slug)
	if !ok {
		return nil, ErrGameNotFound
	}
	return engine.Replay, nil
}

// officialView builds the result screen for an official attempt: the server
// score, where it puts the player on the board and attempts left.
func (s *CompetitionService) officialView(ctx context.Context, sub *models.ScoreSubmission, result *games.Result, fresh bool) (*models.GameScoreView, error) {
	c, err := s.repo.GetByID(ctx, sub.CompetitionID)
	if err != nil {
		return nil, err
	}
	stats, err := s.repo.MyStats(ctx, sub.CustomerID, []uuid.UUID{sub.CompetitionID})
	if err != nil {
		return nil, err
	}
	_, mine, _, err := s.repo.CompetitionLeaderboard(ctx, sub.CompetitionID, 0, &sub.CustomerID)
	if err != nil {
		return nil, err
	}

	st := stats[sub.CompetitionID]
	var best int64
	if st.BestScore != nil {
		best = *st.BestScore
	}
	remaining := c.MaxAttemptsPerCustomer - st.AttemptsUsed
	if remaining < 0 {
		remaining = 0
	}
	accepted := sub.ValidationStatus == "accepted"
	competitionID := sub.CompetitionID

	view := &models.GameScoreView{
		Score:             sub.Score,
		HighestTile:       int(sub.Stats["highest_tile"]),
		MovesCount:        sub.MovesCount,
		Stats:             sub.Stats,
		PersonalBest:      best,
		IsPersonalBest:    fresh && accepted && sub.Score >= best && sub.Score > 0,
		Accepted:          accepted,
		CompetitionID:     &competitionID,
		AttemptsRemaining: &remaining,
	}
	if attempt, err := s.repo.GetAttemptByID(ctx, sub.AttemptID); err == nil {
		view.SessionID = attempt.SessionID
	}
	if mine != nil {
		rank := mine.Rank
		view.Rank = &rank
	}
	if result != nil {
		view.Won = result.Won
		view.GameOver = result.GameOver
	}
	return view, nil
}

// ─── Prize claims (customer) ──────────────────────────────────────────────

// ClaimPrizeInput is the winner accepting their prize.
type ClaimPrizeInput struct {
	AddressID   string `json:"address_id"`
	AcceptTerms bool   `json:"accept_terms"`
}

// ClaimPrize lets a validated winner accept the prize terms and choose where
// it should be delivered, within the 14-day window.
func (s *CompetitionService) ClaimPrize(ctx context.Context, id uuid.UUID, actor SocialActor, in ClaimPrizeInput) (*models.PrizeClaim, error) {
	customerID, err := parseCustomer(actor)
	if err != nil {
		return nil, err
	}
	if customerID == nil {
		return nil, ErrCustomerRequired
	}
	if !in.AcceptTerms {
		return nil, fmt.Errorf("%w: accept the prize terms to claim", ErrInvalidClaim)
	}
	addressID, err := uuid.Parse(in.AddressID)
	if err != nil {
		return nil, fmt.Errorf("%w: choose a delivery address", ErrInvalidClaim)
	}
	owns, err := s.repo.CustomerOwnsAddress(ctx, *customerID, addressID)
	if err != nil {
		return nil, err
	}
	if !owns {
		return nil, fmt.Errorf("%w: choose one of your saved addresses", ErrInvalidClaim)
	}

	winners, err := s.repo.Winners(ctx, id)
	if err != nil {
		return nil, err
	}
	var claim *models.PrizeClaim
	for _, w := range winners {
		if w.CustomerID == *customerID && w.Status == "validated" && w.Claim != nil {
			claim = w.Claim
		}
	}
	if claim == nil {
		return nil, ErrClaimNotFound
	}
	if !s.now().Before(claim.ClaimDeadlineAt) || claim.Status != "pending" {
		return nil, ErrClaimState
	}

	uid := *customerID
	out, err := s.repo.ClaimPrize(ctx, claim.ID, uid, addressID, models.AuditEntry{
		ActorType:  "customer",
		ActorID:    &uid,
		Action:     "competition.prize_claimed",
		EntityType: "prize_claim",
		EntityID:   &claim.ID,
	})
	if errors.Is(err, repository.ErrClaimStateChanged) {
		return nil, ErrClaimState
	}
	return out, err
}

// ─── Admin: setup ─────────────────────────────────────────────────────────

// CompetitionInput is what an admin sets when creating or editing a
// competition. The server seed is never taken from input.
type CompetitionInput struct {
	// The countries the competition runs in; players from any of them may
	// enter once each country's gates allow it.
	CountryIDs                   []string  `json:"country_ids"`
	GameSlug                     string    `json:"game_slug"`
	Title                        string    `json:"title"`
	StartsAt                     time.Time `json:"starts_at"`
	EndsAt                       time.Time `json:"ends_at"`
	Timezone                     string    `json:"timezone"`
	MaxAttemptsPerCustomer       int       `json:"max_attempts_per_customer"`
	MinAge                       int       `json:"min_age"`
	RequiresIdentityVerification *bool     `json:"requires_identity_verification"`
	NumberOfWinners              int       `json:"number_of_winners"`
	PrizeDescription             string    `json:"prize_description"`
	PrizeValueAmount             *int64    `json:"prize_value_amount"`
	PrizeCurrency                *string   `json:"prize_currency"`
	OfficialRules                *string   `json:"official_rules"`

	// Progressive prize economics (spec §2). start_prize_cents falls back to
	// prize_value_amount, which older admin screens still send.
	PrizeGrowthEnabled    bool   `json:"prize_growth_enabled"`
	PrizeType             string `json:"prize_type"`
	WinnerMethod          string `json:"winner_method"`
	StartPrizeCents       *int64 `json:"start_prize_cents"`
	IncrementPerPlayCents int64  `json:"increment_per_play_cents"`
	MaxPrizeCents         *int64 `json:"max_prize_cents"`
	ContinueAtCap         *bool  `json:"continue_at_cap"`
	DailyPlayLimit        *int   `json:"daily_play_limit"`
	MinPlaysToWin         *int   `json:"min_plays_to_win"`
	// Instant-win chance rounds: each play wins with probability 1 in
	// win_odds. Ignored for skill games and prize draws.
	WinOdds *int `json:"win_odds"`
	// Points prizes (prize_type "points"): what each validated winner
	// receives, credited to their points balance on validation.
	PrizePoints *int64 `json:"prize_points"`
	// Quiz rounds: the questions, answers and time limits.
	QuizQuestions []games.QuizQuestion `json:"quiz_questions"`
	// The config_version the admin was editing; a stale edit is refused.
	ConfigVersion *int `json:"config_version"`
}

var prizeTypes = map[string]bool{"cash": true, "product": true, "voucher": true, "gift": true, "points": true, "other": true}

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidCompetition, msg) }

// competitionCountries resolves the chosen country IDs, dropping repeats.
func (s *CompetitionService) competitionCountries(ctx context.Context, ids []string) ([]models.CompetitionCountry, error) {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]models.CompetitionCountry, 0, len(ids))
	for _, raw := range ids {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, invalid("country_ids must be country IDs")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		co, err := s.countries.GetByID(ctx, id.String())
		if err != nil {
			if errors.Is(err, repository.ErrCountryNotFound) {
				return nil, invalid("country " + id.String() + " does not exist")
			}
			return nil, err
		}
		out = append(out, models.CompetitionCountry{
			ID:              co.ID,
			IsoCode:         co.ISOCode,
			Name:            co.Name,
			DefaultCurrency: strings.ToUpper(strings.TrimSpace(co.DefaultCurrency)),
		})
	}
	if len(out) == 0 {
		return nil, invalid("choose at least one country")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// countryIDStrings lists a competition's country IDs as input strings.
func countryIDStrings(c *models.Competition) []string {
	out := make([]string, len(c.Countries))
	for i, co := range c.Countries {
		out[i] = co.ID.String()
	}
	return out
}

// prizeCurrency picks the money prize's currency: one of the chosen
// countries' currencies. It can be left out when they all share one.
func prizeCurrency(countries []models.CompetitionCountry, requested *string) (string, error) {
	var allowed []string
	for _, co := range countries {
		if co.DefaultCurrency == "" {
			return "", invalid(co.Name + " has no currency")
		}
		if !slices.Contains(allowed, co.DefaultCurrency) {
			allowed = append(allowed, co.DefaultCurrency)
		}
	}
	if requested == nil || strings.TrimSpace(*requested) == "" {
		if len(allowed) > 1 {
			return "", invalid("the chosen countries use different currencies (" +
				strings.Join(allowed, ", ") + "); choose prize_currency")
		}
		return allowed[0], nil
	}
	cur := strings.ToUpper(strings.TrimSpace(*requested))
	if !slices.Contains(allowed, cur) {
		return "", invalid("prize_currency must be the currency of one of the chosen countries (" +
			strings.Join(allowed, ", ") + ")")
	}
	return cur, nil
}

// apply validates the input onto a competition.
func (s *CompetitionService) apply(ctx context.Context, c *models.Competition, in CompetitionInput) error {
	countries, err := s.competitionCountries(ctx, in.CountryIDs)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(in.Title)
	if n := len([]rune(title)); n < 3 || n > 120 {
		return invalid("title must be 3 to 120 characters")
	}
	if in.StartsAt.IsZero() || in.EndsAt.IsZero() || !in.EndsAt.After(in.StartsAt) {
		return invalid("ends_at must be after starts_at")
	}
	if strings.TrimSpace(in.Timezone) == "" {
		return invalid("timezone is required")
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return invalid("timezone is not a valid IANA zone")
	}
	if in.MaxAttemptsPerCustomer == 0 {
		in.MaxAttemptsPerCustomer = 3
	}
	if in.MaxAttemptsPerCustomer < 1 || in.MaxAttemptsPerCustomer > 10000 {
		return invalid("max_attempts_per_customer must be 1 to 10000")
	}
	if in.MinAge == 0 {
		in.MinAge = 18
	}
	if in.MinAge < 18 {
		return invalid("min_age cannot be below 18")
	}
	if in.NumberOfWinners == 0 {
		in.NumberOfWinners = 1
	}
	if in.NumberOfWinners < 1 || in.NumberOfWinners > 100 {
		return invalid("number_of_winners must be 1 to 100")
	}
	prize := strings.TrimSpace(in.PrizeDescription)
	if prize == "" {
		return invalid("prize_description is required")
	}
	start := in.StartPrizeCents
	if start == nil {
		start = in.PrizeValueAmount
	}
	if start != nil && *start < 0 {
		return invalid("start_prize_cents cannot be negative")
	}
	if in.PrizeType == "" {
		in.PrizeType = "cash"
	}
	if !prizeTypes[in.PrizeType] {
		return invalid("prize_type must be cash, product, voucher, gift, points or other")
	}
	var prizePoints *int64
	if in.PrizeType == "points" {
		if in.PrizePoints == nil || *in.PrizePoints < 1 || *in.PrizePoints > 100_000_000 {
			return invalid("prize_points must be 1 to 100000000 for a points prize")
		}
		// Points are paid per winner as set; there is no money prize to grow.
		if in.PrizeGrowthEnabled {
			return invalid("a points prize cannot grow")
		}
		n := *in.PrizePoints
		prizePoints = &n
	}
	// The game decides how winners are found: a skill game by the best
	// verified score, a prize draw by drawing entries at close, any other
	// chance game instantly, at the play that wins.
	mechanic, chance := games.ChanceMechanic(in.GameSlug)
	wantMethod := "score"
	switch {
	case chance && mechanic == games.MechanicDraw:
		wantMethod = "draw"
	case chance:
		wantMethod = "instant"
	}
	if in.WinnerMethod == "" {
		in.WinnerMethod = wantMethod
	}
	if in.WinnerMethod != wantMethod {
		return invalid("winner_method must be " + wantMethod + " for this game")
	}
	var winOdds *int
	if wantMethod == "instant" {
		if in.WinOdds == nil || *in.WinOdds < 2 || *in.WinOdds > 100000000 {
			return invalid("win_odds (1 in N) must be 2 to 100000000 for an instant-win game")
		}
		// An instant win closes the round, so there is exactly one winner.
		if in.NumberOfWinners > 1 {
			return invalid("an instant-win round has one winner: the play that wins closes it")
		}
		odds := *in.WinOdds
		winOdds = &odds
	}
	if in.PrizeGrowthEnabled {
		if in.IncrementPerPlayCents <= 0 {
			return invalid("increment_per_play_cents must be positive when the prize grows")
		}
	} else {
		in.IncrementPerPlayCents = 0
		in.MaxPrizeCents = nil
	}
	if in.IncrementPerPlayCents < 0 {
		return invalid("increment_per_play_cents cannot be negative")
	}
	if in.MaxPrizeCents != nil {
		startValue := int64(0)
		if start != nil {
			startValue = *start
		}
		if *in.MaxPrizeCents <= 0 || *in.MaxPrizeCents < startValue {
			return invalid("max_prize_cents must be positive and at least the start prize")
		}
	}
	if in.DailyPlayLimit != nil && (*in.DailyPlayLimit < 1 || *in.DailyPlayLimit > 10000) {
		return invalid("daily_play_limit must be 1 to 10000")
	}
	if in.MinPlaysToWin != nil && (*in.MinPlaysToWin < 1 || *in.MinPlaysToWin > in.MaxAttemptsPerCustomer) {
		return invalid("min_plays_to_win must be 1 to max_attempts_per_customer")
	}
	continueAtCap := true
	if in.ContinueAtCap != nil {
		continueAtCap = *in.ContinueAtCap
	}
	var currency *string
	if in.PrizeType != "points" {
		cur, err := prizeCurrency(countries, in.PrizeCurrency)
		if err != nil {
			return err
		}
		currency = &cur
	} else if in.PrizeCurrency != nil && strings.TrimSpace(*in.PrizeCurrency) != "" {
		cur := strings.ToUpper(strings.TrimSpace(*in.PrizeCurrency))
		if len(cur) != 3 {
			return invalid("prize_currency must be a 3-letter code")
		}
		currency = &cur
	}
	var rules *string
	if in.OfficialRules != nil && strings.TrimSpace(*in.OfficialRules) != "" {
		r := strings.TrimSpace(*in.OfficialRules)
		rules = &r
	}

	// Competitions lock an approved game version that has a replay engine,
	// or one of the chance mechanics.
	quiz := in.GameSlug == games.QuizSlug
	if _, ok := games.EngineFor(in.GameSlug); !ok && !chance && !quiz {
		return invalid("game_slug is not a playable game")
	}
	var questions []games.QuizQuestion
	if quiz {
		for i := range in.QuizQuestions {
			q := &in.QuizQuestions[i]
			q.Prompt = strings.TrimSpace(q.Prompt)
			for j := range q.Options {
				q.Options[j] = strings.TrimSpace(q.Options[j])
			}
			if q.TimeLimitSeconds == 0 {
				q.TimeLimitSeconds = 20
			}
		}
		if err := games.ValidateQuiz(in.QuizQuestions); err != nil {
			return invalid(err.Error())
		}
		questions = in.QuizQuestions
	}
	pg, err := s.games.GetPlayableBySlug(ctx, in.GameSlug)
	if errors.Is(err, repository.ErrGameNotFound) {
		return invalid("game_slug has no approved version")
	}
	if err != nil {
		return err
	}

	requiresID := true
	if in.RequiresIdentityVerification != nil {
		requiresID = *in.RequiresIdentityVerification
	}

	c.Countries = countries
	c.GameVersionID = pg.Version.ID
	c.Title = title
	c.StartsAt = in.StartsAt
	c.EndsAt = in.EndsAt
	c.Timezone = in.Timezone
	// A play costs what the game costs everywhere else, set on the game.
	c.PointsPerAttempt = int(pg.Game.PlayCostPoints)
	c.MaxAttemptsPerCustomer = in.MaxAttemptsPerCustomer
	c.MinAge = in.MinAge
	c.RequiresIdentityVerification = requiresID
	c.NumberOfWinners = in.NumberOfWinners
	c.PrizeDescription = prize
	c.PrizeValueAmount = start
	c.PrizeCurrency = currency
	c.OfficialRules = rules
	c.PrizeGrowthEnabled = in.PrizeGrowthEnabled
	c.PrizeType = in.PrizeType
	c.WinnerMethod = in.WinnerMethod
	c.StartPrizeCents = 0
	if start != nil {
		c.StartPrizeCents = *start
	}
	c.IncrementPerPlayCents = in.IncrementPerPlayCents
	c.MaxPrizeCents = in.MaxPrizeCents
	c.ContinueAtCap = continueAtCap
	c.DailyPlayLimit = in.DailyPlayLimit
	c.MinPlaysToWin = in.MinPlaysToWin
	c.WinOdds = winOdds
	c.PrizePoints = prizePoints
	c.QuizQuestions = questions
	c.GameSlug = in.GameSlug
	return nil
}

// CreateCompetition creates a draft with a fresh server seed.
func (s *CompetitionService) CreateCompetition(ctx context.Context, admin AdminActor, in CompetitionInput) (*models.AdminCompetitionView, error) {
	c := &models.Competition{}
	if err := s.apply(ctx, c, in); err != nil {
		return nil, err
	}
	seed, err := games.NewSeed()
	if err != nil {
		return nil, err
	}
	c.ServerSeed = seed
	adminID := admin.ID
	c.CreatedByAdminID = &adminID
	c.UpdatedByAdminID = &adminID

	audit := admin.audit("competition.created", uuid.Nil, nil)
	if err := s.repo.Create(ctx, c, audit); err != nil {
		if isForeignKeyViolation(err) {
			return nil, invalid("a chosen country does not exist")
		}
		return nil, err
	}
	return s.AdminGet(ctx, c.ID)
}

// UpdateCompetition edits the rules while they are editable: a draft, or a
// scheduled competition that has not started. Editing returns it to draft
// so every schedule gate is checked again.
func (s *CompetitionService) UpdateCompetition(ctx context.Context, admin AdminActor, id uuid.UUID, in CompetitionInput) (*models.AdminCompetitionView, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if !competitionEditable(c.Status, c.StartsAt, s.now()) {
		return nil, ErrCompetitionLocked
	}
	// Editing a scheduled competition takes it back to draft, pulling it
	// from players: that is a Super Admin's call.
	if c.Status != "draft" && !admin.SuperAdmin {
		return nil, ErrSuperAdminOnly
	}
	before := *c
	if err := s.apply(ctx, c, in); err != nil {
		return nil, err
	}
	adminID := admin.ID
	c.UpdatedByAdminID = &adminID
	audit := admin.audit("competition.updated", c.ID, nil)
	audit.Before = before
	if err := s.repo.Update(ctx, c, in.ConfigVersion, audit); err != nil {
		switch {
		case errors.Is(err, repository.ErrCompetitionStateChanged):
			return nil, ErrCompetitionLocked
		case errors.Is(err, repository.ErrConfigVersionConflict):
			return nil, ErrConfigConflict
		case isForeignKeyViolation(err):
			return nil, invalid("a chosen country does not exist")
		}
		return nil, err
	}
	return s.AdminGet(ctx, c.ID)
}

// ReserveInput sets the prize reserve.
type ReserveInput struct {
	ReserveAmount int64  `json:"reserve_amount"`
	Currency      string `json:"currency"`
	FundingSource string `json:"funding_source"`
}

// SetReserve records how much must be held for the prize and who funds it.
// Prizes are funded by SendAgift or an approved sponsor, never by points or
// attempts (§13.12).
func (s *CompetitionService) SetReserve(ctx context.Context, admin AdminActor, id uuid.UUID, in ReserveInput) (*models.PrizeReserve, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if !competitionEditable(c.Status, c.StartsAt, s.now()) {
		return nil, ErrCompetitionLocked
	}
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	if c.PrizeCurrency == nil {
		return nil, fmt.Errorf("%w: the competition has no prize currency", ErrInvalidReserve)
	}
	if in.ReserveAmount <= 0 || cur != *c.PrizeCurrency {
		return nil, fmt.Errorf("%w: reserve_amount must be positive and currency must be the prize currency (%s)", ErrInvalidReserve, *c.PrizeCurrency)
	}
	if in.FundingSource != "sendagift" && in.FundingSource != "approved_sponsor" {
		return nil, fmt.Errorf("%w: funding_source must be sendagift or approved_sponsor", ErrInvalidReserve)
	}
	out, err := s.repo.UpsertReserve(ctx, &models.PrizeReserve{
		CompetitionID: c.ID,
		ReserveAmount: in.ReserveAmount,
		Currency:      cur,
		FundingSource: in.FundingSource,
	}, admin.audit("competition.reserve_set", c.ID, nil))
	if errors.Is(err, repository.ErrReserveLocked) {
		return nil, ErrReserveLocked
	}
	return out, err
}

// FundReserve records that the prize money is actually held, with the
// escrow, purchase or insurance evidence.
func (s *CompetitionService) FundReserve(ctx context.Context, admin AdminActor, id uuid.UUID, evidence string) (*models.PrizeReserve, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	evidence = strings.TrimSpace(evidence)
	if evidence == "" {
		return nil, fmt.Errorf("%w: evidence_reference is required", ErrInvalidReserve)
	}
	out, err := s.repo.FundReserve(ctx, c.ID, evidence, admin.audit("competition.reserve_funded", c.ID, &evidence))
	if errors.Is(err, repository.ErrReserveLocked) {
		return nil, ErrReserveLocked
	}
	return out, err
}

// scheduleBlockers lists everything stopping a draft from being published.
func (s *CompetitionService) scheduleBlockers(ctx context.Context, c *models.Competition) ([]string, error) {
	var blockers []string
	if c.Status != "draft" {
		blockers = append(blockers, "only a draft can be scheduled")
	}
	// A start time already passed means "open it as soon as it is
	// published"; only one that has already ended cannot go out.
	if !c.EndsAt.After(s.now().Add(time.Minute)) {
		blockers = append(blockers, "it has already ended — set a later end time")
	}
	if c.GameVersionStatus != "approved" {
		blockers = append(blockers, "the game version is no longer approved")
	}
	if len(c.Countries) == 0 {
		blockers = append(blockers, "choose at least one country")
	}
	if c.OfficialRules == nil || strings.TrimSpace(*c.OfficialRules) == "" {
		blockers = append(blockers, "official rules must be published")
	}
	// A points prize is valued in points (prize_points), not money.
	if c.PrizeType != "points" && (c.PrizeValueAmount == nil || c.PrizeCurrency == nil) {
		blockers = append(blockers, "prize value and currency are required")
	}
	// Every country the round runs in must pass its own gates.
	cache := capabilityCache{}
	for _, co := range c.Countries {
		cc, err := s.capability(ctx, co.ID, cache)
		if err != nil {
			return nil, err
		}
		if cc == nil || !cc.SkillCompetitionsEnabled {
			blockers = append(blockers, "skill competitions are not enabled for "+co.Name)
		}
		if c.PrizeGrowthEnabled && (cc == nil || !cc.ProgressivePrizesEnabled) {
			blockers = append(blockers, "progressive prizes are not approved for "+co.Name+
				" (needs legal sign-off, then the country's progressive_prizes_enabled gate)")
		}
		if c.PointsPerAttempt > 0 && (cc == nil || !cc.PointsUsageEnabled) {
			blockers = append(blockers, "points usage is not enabled for "+co.Name)
		}
		if c.GameType == "chance" && (cc == nil || !cc.ChanceGamesEnabled) {
			blockers = append(blockers, "games of chance are not approved for "+co.Name+
				" (needs legal sign-off, then the country's chance_games_enabled gate)")
		}
	}
	if c.PrizeGrowthEnabled && c.MaxPrizeCents == nil {
		blockers = append(blockers, "a growing prize needs a maximum prize, so the funded reserve can cover it")
	}
	if c.WinnerMethod == "instant" && c.WinOdds == nil {
		blockers = append(blockers, "an instant-win round needs its win odds")
	}

	// Points prizes are credited by the platform; there is no money to hold
	// in reserve for them.
	if c.PrizeType == "points" {
		return blockers, nil
	}
	// The reserve must hold the most the prize can ever reach: the fixed
	// prize, or the cap of a growing one.
	liability := maxLiability(c)
	reserve, err := s.repo.GetReserve(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	switch {
	case reserve == nil:
		blockers = append(blockers, "a prize reserve is required")
	case reserve.Status != "funded":
		blockers = append(blockers, "the prize reserve must be funded, with evidence")
	case c.PrizeCurrency != nil && liability != nil &&
		(reserve.Currency != *c.PrizeCurrency || reserve.ReserveAmount < *liability):
		blockers = append(blockers, "the funded reserve must cover the maximum prize in the prize currency")
	}
	return blockers, nil
}

// competitionAnnouncement is the push notification players in the
// competition's countries get when it is published: what to play, what can
// be won, and when it opens in the competition's own time zone.
func competitionAnnouncement(c *models.Competition) models.PushMessage {
	opens := "Open now"
	if loc, err := time.LoadLocation(c.Timezone); err == nil && c.StartsAt.After(time.Now()) {
		opens = "Opens " + c.StartsAt.In(loc).Format("Mon 2 Jan, 3:04 PM MST")
	}
	prize := strings.TrimSpace(c.PrizeDescription)
	if c.PrizeType == "points" && c.PrizePoints != nil {
		prize = fmt.Sprintf("%d points", *c.PrizePoints)
	}
	return models.PushMessage{
		Title: "New competition: " + c.Title,
		Body:  fmt.Sprintf("Play %s and win %s. %s.", c.GameName, prize, opens),
		Data: map[string]string{
			"type":           "competition",
			"competition_id": c.ID.String(),
		},
	}
}

// ScheduleCompetition publishes a draft once every gate passes: every
// country enabled, rules published, prize reserve funded and covering the prize.
func (s *CompetitionService) ScheduleCompetition(ctx context.Context, admin AdminActor, id uuid.UUID) (*models.AdminCompetitionView, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	blockers, err := s.scheduleBlockers(ctx, c)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrScheduleBlocked, strings.Join(blockers, "; "))
	}
	if err := s.repo.Schedule(ctx, c.ID, admin.ledgerActor(), admin.audit("competition.scheduled", c.ID, nil),
		competitionAnnouncement(c)); err != nil {
		if errors.Is(err, repository.ErrCompetitionStateChanged) {
			return nil, ErrCompetitionState
		}
		return nil, err
	}
	return s.AdminGet(ctx, c.ID)
}

// CancelInput is a cancellation with its permitted reason.
type CancelInput struct {
	Reason string  `json:"reason"`
	Note   *string `json:"note"`
}

// CancelCompetition stops a competition for a permitted reason only (§13.9).
// No winner is declared and every attempt is voided.
func (s *CompetitionService) CancelCompetition(ctx context.Context, admin AdminActor, id uuid.UUID, in CancelInput) (*models.AdminCompetitionView, error) {
	if !cancelReasons[in.Reason] {
		return nil, invalid("reason must be one of technical_failure, security_breach, legal_direction, " +
			"provider_or_store_direction, platform_outage, prize_unavailable, fairness_failure")
	}
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	reason := in.Reason
	if err := s.repo.Cancel(ctx, c.ID, in.Reason, in.Note, admin.ledgerActor(), admin.audit("competition.cancelled", c.ID, &reason)); err != nil {
		if errors.Is(err, repository.ErrCompetitionStateChanged) {
			return nil, ErrCompetitionState
		}
		return nil, err
	}
	return s.AdminGet(ctx, c.ID)
}

// ─── Admin: close, review and finalise ────────────────────────────────────

// FreezeCompetition locks the board after close and snapshots it (§17).
func (s *CompetitionService) FreezeCompetition(ctx context.Context, admin AdminActor, id uuid.UUID) (*models.AdminCompetitionView, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status != "closed" {
		return nil, ErrCompetitionState
	}
	snapshot, err := s.snapshot(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	adminID := admin.ID
	if err := s.repo.Freeze(ctx, c.ID, snapshot, &adminID, admin.audit("competition.frozen", c.ID, nil)); err != nil {
		if errors.Is(err, repository.ErrCompetitionStateChanged) {
			return nil, ErrCompetitionState
		}
		return nil, err
	}
	return s.AdminGet(ctx, c.ID)
}

func (s *CompetitionService) snapshot(ctx context.Context, id uuid.UUID) ([]byte, error) {
	rows, _, _, err := s.repo.CompetitionLeaderboard(ctx, id, snapshotLimit, nil)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []repository.LeaderRow{}
	}
	return json.Marshal(rows)
}

// ReviewInput is an admin's verdict on a held score.
type ReviewInput struct {
	Status string  `json:"status"` // accepted or rejected
	Reason *string `json:"reason"`
}

// ReviewSubmission accepts or rejects a score held for review.
func (s *CompetitionService) ReviewSubmission(ctx context.Context, admin AdminActor, id, submissionID uuid.UUID, in ReviewInput) error {
	if in.Status != "accepted" && in.Status != "rejected" {
		return fmt.Errorf("%w: status must be accepted or rejected", ErrInvalidReview)
	}
	if in.Status == "rejected" && (in.Reason == nil || strings.TrimSpace(*in.Reason) == "") {
		return fmt.Errorf("%w: a reason is required to reject a score", ErrInvalidReview)
	}
	c, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	adminID := admin.ID
	audit := admin.audit("competition.score_reviewed", c.ID, in.Reason)
	audit.After = map[string]any{"submission_id": submissionID, "status": in.Status}
	err = s.repo.ReviewSubmission(ctx, c.ID, submissionID, in.Status, in.Reason, &adminID, audit)
	if errors.Is(err, repository.ErrSubmissionNotFound) {
		return ErrCompetitionState
	}
	return err
}

// ReviewQueue lists scores awaiting a decision.
func (s *CompetitionService) ReviewQueue(ctx context.Context, id uuid.UUID) ([]models.ScoreSubmission, error) {
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.ReviewQueue(ctx, id)
}

// FinaliseInput optionally carries the result of a skill playoff, used only
// when a tie decides who gets which prize.
type FinaliseInput struct {
	Playoff []struct {
		CustomerID uuid.UUID `json:"customer_id"`
		Position   int       `json:"position"`
	} `json:"playoff"`
}

// FinaliseCompetition declares the result (§17–§19):
//  1. every score must be reviewed;
//  2. the leading scores are replayed again and any that no longer match are
//     rejected;
//  3. winners are taken in rank order, skipping ineligible players, with ties
//     at the cutoff refused unless a playoff settles them;
//  4. winners and the final snapshot are written, and the competition is
//     finalised.
func (s *CompetitionService) FinaliseCompetition(ctx context.Context, admin AdminActor, id uuid.UUID, in FinaliseInput) (*models.AdminCompetitionView, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	switch c.WinnerMethod {
	case "instant":
		// The winner, if a play won, was recorded at the moment it won.
		err := s.repo.FinaliseInstant(ctx, c.ID, admin.audit("competition.finalised", c.ID, nil))
		if errors.Is(err, repository.ErrCompetitionStateChanged) {
			return nil, ErrCompetitionState
		}
		if err != nil {
			return nil, err
		}
		return s.AdminGet(ctx, c.ID)
	case "draw":
		return nil, fmt.Errorf("%w: a prize draw is finalised by running the draw", ErrCompetitionState)
	}
	if c.Status != "frozen" {
		return nil, ErrCompetitionState
	}
	unresolved, err := s.repo.CountUnresolved(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	if unresolved > 0 {
		return nil, fmt.Errorf("%w: %d left", ErrUnresolvedScores, unresolved)
	}
	scorer, err := s.scorer(ctx, c.ID, c.GameSlug)
	if err != nil {
		return nil, err
	}
	adminID := admin.ID

	// Re-verify. Rejecting a score can lift another into range, so repeat
	// until the leading scores all replay cleanly.
	var ranked []repository.RankedSubmission
	for round := 0; ; round++ {
		if ranked, err = s.repo.RankedAccepted(ctx, c.ID, c.NumberOfWinners+finaliseWindow); err != nil {
			return nil, err
		}
		var bad []uuid.UUID
		for _, sub := range ranked {
			res, err := scorer(c.ServerSeed, c.GameConfig, sub.EventLog)
			if err != nil || res.Score != sub.Score {
				bad = append(bad, sub.SubmissionID)
			}
		}
		if len(bad) == 0 {
			break
		}
		if round >= 5 {
			return nil, fmt.Errorf("re-verification keeps failing for %d scores", len(bad))
		}
		audit := admin.audit("competition.scores_rejected_on_reverify", c.ID, nil)
		audit.After = bad
		if err := s.repo.RejectSubmissions(ctx, c.ID, bad, "failed re-verification at finalisation", &adminID, audit); err != nil {
			return nil, err
		}
	}

	candidates, err := s.candidates(ctx, c, ranked, nil)
	if err != nil {
		return nil, err
	}
	playoff := map[uuid.UUID]int{}
	for _, p := range in.Playoff {
		playoff[p.CustomerID] = p.Position
	}
	plans, err := planWinners(candidates, c.NumberOfWinners, playoff)
	if err != nil {
		return nil, err
	}
	values := winnerPrizeValues(c)
	pricePlans(plans, func(position int) *int64 {
		if position < 1 || position > len(values) {
			return nil
		}
		v := values[position-1]
		return &v
	})

	snapshot, err := s.snapshot(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	audit := admin.audit("competition.finalised", c.ID, nil)
	audit.After = map[string]any{"winners": plans, "playoff": in.Playoff}
	if err := s.repo.ApplyFinalisation(ctx, c.ID, plans, snapshot, &adminID, audit); err != nil {
		if errors.Is(err, repository.ErrCompetitionStateChanged) {
			return nil, ErrCompetitionState
		}
		return nil, err
	}
	return s.AdminGet(ctx, c.ID)
}

// candidates checks each ranked player's eligibility, skipping anyone in
// exclude (players who already hold or held a winner row).
func (s *CompetitionService) candidates(ctx context.Context, c *models.Competition, ranked []repository.RankedSubmission, exclude map[uuid.UUID]bool) ([]winnerCandidate, error) {
	cache := capabilityCache{}
	out := make([]winnerCandidate, 0, len(ranked))
	var plays map[uuid.UUID]int
	if c.MinPlaysToWin != nil {
		ids := make([]uuid.UUID, 0, len(ranked))
		for _, sub := range ranked {
			ids = append(ids, sub.CustomerID)
		}
		var err error
		if plays, err = s.repo.PlayCounts(ctx, c.ID, ids); err != nil {
			return nil, err
		}
	}
	for _, sub := range ranked {
		if exclude[sub.CustomerID] {
			continue
		}
		cand := winnerCandidate{CustomerID: sub.CustomerID, SubmissionID: sub.SubmissionID, Rank: sub.Rank}
		cust, err := s.customers.GetByID(ctx, sub.CustomerID.String())
		switch {
		case errors.Is(err, repository.ErrCustomerNotFound):
			cand.Reason = "account closed"
		case err != nil:
			return nil, err
		default:
			ok, reason, err := s.eligibility(ctx, c, cust, cache)
			if err != nil {
				return nil, err
			}
			if ok && c.MinPlaysToWin != nil && plays[sub.CustomerID] < *c.MinPlaysToWin {
				ok, reason = false, fmt.Sprintf("played %d of the %d plays needed to win",
					plays[sub.CustomerID], *c.MinPlaysToWin)
			}
			cand.Eligible, cand.Reason = ok, reason
		}
		out = append(out, cand)
	}
	return out, nil
}

// ─── Admin: winners and claims ────────────────────────────────────────────

func (s *CompetitionService) finalisedWinner(ctx context.Context, id, winnerID uuid.UUID) (*models.Competition, *models.CompetitionWinner, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if c.Status != "finalised" {
		return nil, nil, ErrCompetitionState
	}
	w, err := s.repo.WinnerByID(ctx, id, winnerID)
	if errors.Is(err, repository.ErrWinnerNotFound) {
		return nil, nil, ErrWinnerNotFound
	}
	return c, w, err
}

// ValidateWinner confirms a winner after checking they still meet every
// rule, and opens the 14-day claim window (§19.2).
func (s *CompetitionService) ValidateWinner(ctx context.Context, admin AdminActor, id, winnerID uuid.UUID) error {
	c, w, err := s.finalisedWinner(ctx, id, winnerID)
	if err != nil {
		return err
	}
	if w.Status != "pending_validation" {
		return ErrWinnerState
	}
	cust, err := s.customer(ctx, w.CustomerID)
	if err != nil {
		return err
	}
	ok, reason, err := s.eligibility(ctx, c, cust, capabilityCache{})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s — disqualify this winner instead", ErrNotEligible, reason)
	}
	audit := admin.audit("competition.winner_validated", c.ID, nil)
	audit.After = map[string]any{"winner_id": w.ID, "prize_position": w.PrizePosition}
	err = s.repo.ValidateWinner(ctx, w.ID, s.now().Add(prizeClaimWindow), audit)
	if errors.Is(err, repository.ErrWinnerStateChanged) {
		return ErrWinnerState
	}
	return err
}

// DisqualifyWinner removes a winner with a recorded reason and passes the
// prize to the next eligible player — never a random pick (§19.3).
func (s *CompetitionService) DisqualifyWinner(ctx context.Context, admin AdminActor, id, winnerID uuid.UUID, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: a reason is required", ErrInvalidReview)
	}
	c, w, err := s.finalisedWinner(ctx, id, winnerID)
	if err != nil {
		return err
	}
	if w.Status != "pending_validation" && w.Status != "validated" {
		return ErrWinnerState
	}
	if w.SettlementStatus == "settled" {
		return ErrWinnerSettled
	}
	return s.replace(ctx, admin, c, w, "disqualified", reason, "rejected",
		[]string{"pending_validation", "validated"}, "competition.winner_disqualified")
}

// MarkUnclaimed retires a validated winner whose 14-day window passed
// without a claim, and passes the prize on (§19.2–§19.3).
func (s *CompetitionService) MarkUnclaimed(ctx context.Context, admin AdminActor, id, winnerID uuid.UUID) error {
	c, w, err := s.finalisedWinner(ctx, id, winnerID)
	if err != nil {
		return err
	}
	if w.Status != "validated" || w.Claim == nil || w.Claim.Status != "pending" {
		return ErrWinnerState
	}
	if w.SettlementStatus == "settled" {
		return ErrWinnerSettled
	}
	if s.now().Before(w.Claim.ClaimDeadlineAt) {
		return fmt.Errorf("%w: the claim window is still open", ErrWinnerState)
	}
	return s.replace(ctx, admin, c, w, "unclaimed", "not claimed within 14 days", "expired",
		[]string{"validated"}, "competition.winner_unclaimed")
}

func (s *CompetitionService) replace(ctx context.Context, admin AdminActor, c *models.Competition, w *models.CompetitionWinner, newStatus, reason, claimStatus string, from []string, action string) error {
	existing, err := s.repo.Winners(ctx, c.ID)
	if err != nil {
		return err
	}
	exclude := map[uuid.UUID]bool{}
	for _, e := range existing {
		exclude[e.CustomerID] = true
	}
	ranked, err := s.repo.RankedAccepted(ctx, c.ID, replacementWindow)
	if err != nil {
		return err
	}
	cands, err := s.candidates(ctx, c, ranked, exclude)
	if err != nil {
		return err
	}
	plans, err := planWinners(cands, 1, nil)
	if err != nil {
		return err
	}
	for i := range plans {
		plans[i].PrizePosition = w.PrizePosition
	}
	pricePlans(plans, func(int) *int64 { return w.PrizeValueCents })

	audit := admin.audit(action, c.ID, &reason)
	audit.Before = map[string]any{"winner_id": w.ID, "status": w.Status}
	audit.After = map[string]any{"status": newStatus, "replacements": plans}
	err = s.repo.ReplaceWinner(ctx, c.ID, w.ID, newStatus, reason, claimStatus, from, plans, audit)
	if errors.Is(err, repository.ErrWinnerStateChanged) {
		return ErrWinnerState
	}
	return err
}

// AdvanceClaim moves a prize claim to verified or fulfilled.
func (s *CompetitionService) AdvanceClaim(ctx context.Context, admin AdminActor, id, claimID uuid.UUID, to string) (*models.PrizeClaim, error) {
	var from []string
	switch to {
	case "verified":
		from = []string{"claimed"}
	case "fulfilled":
		from = []string{"verified"}
	default:
		return nil, ErrClaimState
	}
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	out, err := s.repo.UpdateClaimStatus(ctx, id, claimID, from, to, admin.audit("competition.claim_"+to, id, nil))
	if errors.Is(err, repository.ErrClaimStateChanged) {
		return nil, ErrClaimState
	}
	return out, err
}

// ─── Admin: reads ─────────────────────────────────────────────────────────

// AdminList returns every competition, drafts included.
func (s *CompetitionService) AdminList(ctx context.Context) ([]models.AdminCompetitionView, error) {
	if err := s.repo.SyncStatuses(ctx); err != nil {
		return nil, err
	}
	list, err := s.repo.List(ctx, repository.CompetitionFilter{IncludeDrafts: true, Limit: 200})
	if err != nil {
		return nil, err
	}
	out := make([]models.AdminCompetitionView, 0, len(list))
	for _, c := range list {
		out = append(out, models.AdminCompetitionView{Competition: c, EffectiveStatus: s.status(&c)})
	}
	return out, nil
}

// AdminGet returns one competition with its reserve and activity counts.
func (s *CompetitionService) AdminGet(ctx context.Context, id uuid.UUID) (*models.AdminCompetitionView, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	reserve, err := s.repo.GetReserve(ctx, id)
	if err != nil {
		return nil, err
	}
	attempts, submissions, review, err := s.repo.AdminCounts(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.GameSlug == games.QuizSlug {
		if c.QuizQuestions, err = s.repo.QuizQuestions(ctx, id); err != nil {
			return nil, err
		}
	}
	announcement, err := s.repo.AnnouncementStats(ctx, id)
	if err != nil {
		return nil, err
	}
	blockers := []string{}
	if c.Status == "draft" {
		if blockers, err = s.scheduleBlockers(ctx, c); err != nil {
			return nil, err
		}
		if blockers == nil {
			blockers = []string{}
		}
	}
	return &models.AdminCompetitionView{
		Competition:      *c,
		EffectiveStatus:  s.status(c),
		Reserve:          reserve,
		Attempts:         attempts,
		Submissions:      submissions,
		UnderReview:      review,
		ScheduleBlockers: blockers,
		Announcement:     announcement,
	}, nil
}

// AdminLeaderboard is the full board with customer ids, for finalising and
// arranging playoffs.
func (s *CompetitionService) AdminLeaderboard(ctx context.Context, id uuid.UUID) ([]repository.LeaderRow, error) {
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	rows, _, _, err := s.repo.CompetitionLeaderboard(ctx, id, snapshotLimit, nil)
	if rows == nil {
		rows = []repository.LeaderRow{}
	}
	return rows, err
}

// AdminWinners lists every winner row, current and historical.
func (s *CompetitionService) AdminWinners(ctx context.Context, id uuid.UUID) ([]models.CompetitionWinner, error) {
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.Winners(ctx, id)
}
