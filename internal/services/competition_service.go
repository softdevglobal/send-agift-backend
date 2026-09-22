package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// PointsDebiter takes the published points for an official attempt.
//
// There is no points ledger yet, so the wired implementation is DisabledPoints:
// attempts cost nothing and points_spent is recorded as 0. The competition
// still publishes its points_per_attempt so the rule is fixed before start.
type PointsDebiter interface {
	Enabled() bool
	Debit(ctx context.Context, customerID, competitionID uuid.UUID, points int, idempotencyKey string) (*uuid.UUID, error)
}

// DisabledPoints is the PointsDebiter used until the points ledger exists.
type DisabledPoints struct{}

func (DisabledPoints) Enabled() bool { return false }

func (DisabledPoints) Debit(context.Context, uuid.UUID, uuid.UUID, int, string) (*uuid.UUID, error) {
	return nil, nil
}

// AdminActor is who performed an admin action, for the audit log.
type AdminActor struct {
	ID        uuid.UUID
	IP        string
	UserAgent string
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
	points       PointsDebiter
	now          func() time.Time
}

func NewCompetitionService(
	repo *repository.CompetitionRepository,
	gameRepo *repository.GameRepository,
	customers *repository.CustomerRepository,
	capabilities *repository.CountryCapabilityRepository,
	points PointsDebiter,
) *CompetitionService {
	if points == nil {
		points = DisabledPoints{}
	}
	return &CompetitionService{
		repo:         repo,
		games:        gameRepo,
		customers:    customers,
		capabilities: capabilities,
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
// within one request.
type capabilityCache map[uuid.UUID]bool

func (s *CompetitionService) competitionsEnabled(ctx context.Context, countryID uuid.UUID, cache capabilityCache) (bool, error) {
	if enabled, ok := cache[countryID]; ok {
		return enabled, nil
	}
	cc, err := s.capabilities.GetByCountryID(ctx, countryID.String())
	if errors.Is(err, repository.ErrCountryCapabilityNotFound) {
		cache[countryID] = false
		return false, nil
	}
	if err != nil {
		return false, err
	}
	cache[countryID] = cc.SkillCompetitionsEnabled
	return cc.SkillCompetitionsEnabled, nil
}

// eligibility checks a customer against a competition's published rules:
// country, the skill-competition gate, 18+ with verified age, and identity
// verification when the rules require it (§13.5). The returned reason is
// written for the player.
func (s *CompetitionService) eligibility(ctx context.Context, c *models.Competition, cust *models.Customer, cache capabilityCache) (bool, string, error) {
	if cust.Status != "active" {
		return false, "Your account is not active.", nil
	}
	if cust.CountryID != c.CountryID {
		return false, fmt.Sprintf("This competition is only open to players in %s.", c.CountryName), nil
	}
	enabled, err := s.competitionsEnabled(ctx, c.CountryID, cache)
	if err != nil {
		return false, "", err
	}
	if !enabled {
		return false, "Skill competitions are not available in your country yet.", nil
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
		CountryCode:                  c.CountryCode,
		CountryName:                  c.CountryName,
		StartsAt:                     c.StartsAt,
		EndsAt:                       c.EndsAt,
		Timezone:                     c.Timezone,
		PointsPerAttempt:             c.PointsPerAttempt,
		PointsDeductionEnabled:       s.points.Enabled(),
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
	return &models.CompetitionMe{
		AttemptsUsed:      stat.AttemptsUsed,
		AttemptsRemaining: remaining,
		BestScore:         stat.BestScore,
		Eligible:          eligible,
		IneligibleReason:  reason,
	}, nil
}

// ListCompetitions returns competitions for the app. Signed-in customers see
// their own country's competitions with their attempts and eligibility;
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

// StartAttempt opens one official attempt. Every entrant is given the
// competition's single seed, so everyone plays the identical game (§14.1).
func (s *CompetitionService) StartAttempt(ctx context.Context, id uuid.UUID, actor SocialActor) (*models.AttemptStartView, error) {
	customerID, err := parseCustomer(actor)
	if err != nil {
		return nil, err
	}
	if customerID == nil {
		return nil, ErrCustomerRequired
	}

	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	switch s.status(c) {
	case "live":
	case "draft":
		return nil, ErrCompetitionNotFound
	case "scheduled":
		return nil, ErrCompetitionNotLive
	default:
		return nil, ErrCompetitionClosed
	}
	if _, ok := games.EngineFor(c.GameSlug); !ok {
		return nil, ErrGameNotFound
	}

	cust, err := s.customer(ctx, *customerID)
	if err != nil {
		return nil, err
	}
	eligible, reason, err := s.eligibility(ctx, c, cust, capabilityCache{})
	if err != nil {
		return nil, err
	}
	if !eligible {
		return nil, fmt.Errorf("%w: %s", ErrNotEligible, reason)
	}

	stats, err := s.repo.MyStats(ctx, cust.ID, []uuid.UUID{c.ID})
	if err != nil {
		return nil, err
	}
	if stats[c.ID].AttemptsUsed >= c.MaxAttemptsPerCustomer {
		return nil, ErrAttemptLimit
	}

	// Points are taken only once the ledger exists (see PointsDebiter).
	var ledgerID *uuid.UUID
	spent := 0
	if s.points.Enabled() && c.PointsPerAttempt > 0 {
		key := fmt.Sprintf("competition:%s:%s:%d", c.ID, cust.ID, stats[c.ID].AttemptsUsed+1)
		if ledgerID, err = s.points.Debit(ctx, cust.ID, c.ID, c.PointsPerAttempt, key); err != nil {
			return nil, err
		}
		spent = c.PointsPerAttempt
	}

	// The score must be received before the competition closes (§13.6), so
	// the session never outlives the competition.
	now := s.now()
	expires := now.Add(time.Duration(games.SessionTTLSeconds(c.GameConfig)) * time.Second)
	if expires.After(c.EndsAt) {
		expires = c.EndsAt
	}

	attempt, session, err := s.repo.CreateAttempt(ctx, repository.CreateAttemptInput{
		CompetitionID:  c.ID,
		CustomerID:     cust.ID,
		GameVersionID:  c.GameVersionID,
		Seed:           c.ServerSeed,
		Config:         c.GameConfig,
		ExpiresAt:      expires,
		MaxAttempts:    c.MaxAttemptsPerCustomer,
		PointsSpent:    spent,
		PointsLedgerID: ledgerID,
	})
	switch {
	case errors.Is(err, repository.ErrAttemptLimitReached):
		return nil, ErrAttemptLimit
	case errors.Is(err, repository.ErrAttemptConflict):
		return nil, ErrAttemptBusy
	case err != nil:
		return nil, err
	}

	remaining := c.MaxAttemptsPerCustomer - (stats[c.ID].AttemptsUsed + 1)
	if remaining < 0 {
		remaining = 0
	}
	return &models.AttemptStartView{
		AttemptID:         attempt.ID,
		AttemptNumber:     attempt.AttemptNumber,
		AttemptsRemaining: remaining,
		PointsSpent:       attempt.PointsSpent,
		Session: models.GameSessionView{
			SessionID: session.ID,
			GameSlug:  c.GameSlug,
			Version:   c.GameVersion,
			Mode:      session.Mode,
			Seed:      session.ServerSeed,
			Config:    session.Config,
			StartedAt: session.StartedAt,
			ExpiresAt: session.ExpiresAt,
		},
	}, nil
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
	now := s.now()
	if s.status(c) != "live" {
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

	engine, ok := games.EngineFor(session.GameSlug)
	if !ok {
		return nil, ErrGameNotFound
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

	result, replayErr := engine.Replay(session.ServerSeed, session.Config, in.Moves)
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
	CountryID                    string    `json:"country_id"`
	GameSlug                     string    `json:"game_slug"`
	Title                        string    `json:"title"`
	StartsAt                     time.Time `json:"starts_at"`
	EndsAt                       time.Time `json:"ends_at"`
	Timezone                     string    `json:"timezone"`
	PointsPerAttempt             int       `json:"points_per_attempt"`
	MaxAttemptsPerCustomer       int       `json:"max_attempts_per_customer"`
	MinAge                       int       `json:"min_age"`
	RequiresIdentityVerification *bool     `json:"requires_identity_verification"`
	NumberOfWinners              int       `json:"number_of_winners"`
	PrizeDescription             string    `json:"prize_description"`
	PrizeValueAmount             *int64    `json:"prize_value_amount"`
	PrizeCurrency                *string   `json:"prize_currency"`
	OfficialRules                *string   `json:"official_rules"`
}

func invalid(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidCompetition, msg) }

// apply validates the input onto a competition.
func (s *CompetitionService) apply(ctx context.Context, c *models.Competition, in CompetitionInput) error {
	countryID, err := uuid.Parse(in.CountryID)
	if err != nil {
		return invalid("country_id is required")
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
	if in.PointsPerAttempt < 0 {
		return invalid("points_per_attempt cannot be negative")
	}
	if in.MaxAttemptsPerCustomer == 0 {
		in.MaxAttemptsPerCustomer = 3
	}
	if in.MaxAttemptsPerCustomer < 1 || in.MaxAttemptsPerCustomer > 100 {
		return invalid("max_attempts_per_customer must be 1 to 100")
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
	if in.PrizeValueAmount != nil && *in.PrizeValueAmount < 0 {
		return invalid("prize_value_amount cannot be negative")
	}
	var currency *string
	if in.PrizeCurrency != nil && strings.TrimSpace(*in.PrizeCurrency) != "" {
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

	// Competitions lock an approved game version that has a replay engine.
	if _, ok := games.EngineFor(in.GameSlug); !ok {
		return invalid("game_slug is not a playable game")
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

	c.CountryID = countryID
	c.GameVersionID = pg.Version.ID
	c.Title = title
	c.StartsAt = in.StartsAt
	c.EndsAt = in.EndsAt
	c.Timezone = in.Timezone
	c.PointsPerAttempt = in.PointsPerAttempt
	c.MaxAttemptsPerCustomer = in.MaxAttemptsPerCustomer
	c.MinAge = in.MinAge
	c.RequiresIdentityVerification = requiresID
	c.NumberOfWinners = in.NumberOfWinners
	c.PrizeDescription = prize
	c.PrizeValueAmount = in.PrizeValueAmount
	c.PrizeCurrency = currency
	c.OfficialRules = rules
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

	audit := admin.audit("competition.created", uuid.Nil, nil)
	if err := s.repo.Create(ctx, c, audit); err != nil {
		if isForeignKeyViolation(err) {
			return nil, invalid("country_id does not exist")
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
	before := *c
	if err := s.apply(ctx, c, in); err != nil {
		return nil, err
	}
	audit := admin.audit("competition.updated", c.ID, nil)
	audit.Before = before
	if err := s.repo.Update(ctx, c, audit); err != nil {
		switch {
		case errors.Is(err, repository.ErrCompetitionStateChanged):
			return nil, ErrCompetitionLocked
		case isForeignKeyViolation(err):
			return nil, invalid("country_id does not exist")
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
	if in.ReserveAmount <= 0 || len(cur) != 3 {
		return nil, fmt.Errorf("%w: reserve_amount must be positive and currency a 3-letter code", ErrInvalidReserve)
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
	if !c.StartsAt.After(s.now()) {
		blockers = append(blockers, "starts_at must be in the future")
	}
	if c.GameVersionStatus != "approved" {
		blockers = append(blockers, "the game version is no longer approved")
	}
	enabled, err := s.competitionsEnabled(ctx, c.CountryID, capabilityCache{})
	if err != nil {
		return nil, err
	}
	if !enabled {
		blockers = append(blockers, "skill competitions are not enabled for "+c.CountryName)
	}
	if c.OfficialRules == nil || strings.TrimSpace(*c.OfficialRules) == "" {
		blockers = append(blockers, "official rules must be published")
	}
	if c.PrizeValueAmount == nil || c.PrizeCurrency == nil {
		blockers = append(blockers, "prize value and currency are required")
	}

	reserve, err := s.repo.GetReserve(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	switch {
	case reserve == nil:
		blockers = append(blockers, "a prize reserve is required")
	case reserve.Status != "funded":
		blockers = append(blockers, "the prize reserve must be funded, with evidence")
	case c.PrizeValueAmount != nil && c.PrizeCurrency != nil &&
		(reserve.Currency != *c.PrizeCurrency || reserve.ReserveAmount < *c.PrizeValueAmount):
		blockers = append(blockers, "the funded reserve must cover the prize value in the prize currency")
	}
	return blockers, nil
}

// ScheduleCompetition publishes a draft once every gate passes: country
// enabled, rules published, prize reserve funded and covering the prize.
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
	if err := s.repo.Schedule(ctx, c.ID, admin.audit("competition.scheduled", c.ID, nil)); err != nil {
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
	if err := s.repo.Cancel(ctx, c.ID, in.Reason, in.Note, admin.audit("competition.cancelled", c.ID, &reason)); err != nil {
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
	engine, ok := games.EngineFor(c.GameSlug)
	if !ok {
		return nil, ErrGameNotFound
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
			res, err := engine.Replay(c.ServerSeed, c.GameConfig, sub.EventLog)
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
	return &models.AdminCompetitionView{
		Competition:     *c,
		EffectiveStatus: s.status(c),
		Reserve:         reserve,
		Attempts:        attempts,
		Submissions:     submissions,
		UnderReview:     review,
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
