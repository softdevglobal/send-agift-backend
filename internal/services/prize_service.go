package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/games"
	"myapp/internal/models"
	"myapp/internal/repository"
)

// Super Admin operations on a round's prize and lifecycle (Progressive Prize
// spec §2.1, §8–§10). Every one of them is a ledger entry or a state change
// written together with its audit row; none edits history.

func (s *CompetitionService) lifecycle(ctx context.Context, admin AdminActor, id uuid.UUID, action string,
	fn func(context.Context, uuid.UUID, models.AuditEntry) error) (*models.AdminCompetitionView, error) {
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	if err := fn(ctx, id, admin.audit(action, id, nil)); err != nil {
		if errors.Is(err, repository.ErrCompetitionStateChanged) {
			return nil, ErrCompetitionState
		}
		if errors.Is(err, repository.ErrCompetitionNotFound) {
			return nil, ErrCompetitionNotFound
		}
		return nil, err
	}
	return s.AdminGet(ctx, id)
}

// PauseCompetition stops new plays on a live round. Plays already accepted
// can still be finished and scored.
func (s *CompetitionService) PauseCompetition(ctx context.Context, admin AdminActor, id uuid.UUID) (*models.AdminCompetitionView, error) {
	return s.lifecycle(ctx, admin, id, "competition.paused", s.repo.Pause)
}

// ResumeCompetition reopens a paused round before its end time.
func (s *CompetitionService) ResumeCompetition(ctx context.Context, admin AdminActor, id uuid.UUID) (*models.AdminCompetitionView, error) {
	return s.lifecycle(ctx, admin, id, "competition.resumed", s.repo.Resume)
}

// CloseCompetition ends a running round early; no new plays after this.
func (s *CompetitionService) CloseCompetition(ctx context.Context, admin AdminActor, id uuid.UUID) (*models.AdminCompetitionView, error) {
	return s.lifecycle(ctx, admin, id, "competition.closed", s.repo.Close)
}

// AdjustPrizeInput is a Super Admin changing the prize. The reason is
// mandatory and kept on the ledger for good.
type AdjustPrizeInput struct {
	AmountDeltaCents int64  `json:"amount_delta_cents"`
	Reason           string `json:"reason"`
}

// AdjustPrize posts an ADMIN_ADJUSTMENT: e.g. $200 → $250 is +5000 with a
// reason, never an edit of the seed or earlier rows (spec §8).
func (s *CompetitionService) AdjustPrize(ctx context.Context, admin AdminActor, id uuid.UUID, in AdjustPrizeInput) (*models.PrizeLedgerEntry, error) {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, fmt.Errorf("%w: a reason is required", ErrInvalidAdjustment)
	}
	if in.AmountDeltaCents == 0 {
		return nil, fmt.Errorf("%w: amount_delta_cents cannot be zero", ErrInvalidAdjustment)
	}
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	audit := admin.audit("competition.prize_adjusted", id, &reason)
	entry, err := s.repo.AdjustPrize(ctx, id, in.AmountDeltaCents, reason, admin.ledgerActor(), audit)
	switch {
	case errors.Is(err, repository.ErrPrizeOutOfRange):
		return nil, ErrPrizeOutOfRange
	case errors.Is(err, repository.ErrCompetitionStateChanged):
		return nil, ErrCompetitionState
	}
	return entry, err
}

// VoidPlayInput voids one play. Refunding points and reversing the prize
// increment both default to on; either can be turned off by policy.
type VoidPlayInput struct {
	Reason       string `json:"reason"`
	RefundPoints *bool  `json:"refund_points"`
	ReversePrize *bool  `json:"reverse_prize"`
}

// VoidPlay voids a play: it leaves the leaderboard, and its points and prize
// increment are returned as compensating entries (spec §8, AC-10).
func (s *CompetitionService) VoidPlay(ctx context.Context, admin AdminActor, id, playID uuid.UUID, in VoidPlayInput) error {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return fmt.Errorf("%w: a reason is required", ErrInvalidAdjustment)
	}
	refund, reverse := true, true
	if in.RefundPoints != nil {
		refund = *in.RefundPoints
	}
	if in.ReversePrize != nil {
		reverse = *in.ReversePrize
	}
	if _, err := s.load(ctx, id); err != nil {
		return err
	}
	audit := admin.audit("competition.play_voided", id, &reason)
	err := s.repo.VoidPlay(ctx, id, playID, reason, refund, reverse, admin.ledgerActor(), audit)
	switch {
	case errors.Is(err, repository.ErrPlayNotFound):
		return ErrPlayNotFound
	case errors.Is(err, repository.ErrPlayState):
		return ErrPlayState
	case errors.Is(err, repository.ErrPrizeOutOfRange):
		return ErrPrizeOutOfRange
	case errors.Is(err, repository.ErrCompetitionStateChanged):
		return ErrCompetitionState
	}
	return err
}

// SettleInput records the payout reference (bank transfer, voucher batch).
type SettleInput struct {
	Reference *string `json:"reference"`
}

// SettleWinners records the payout of every validated winner on the prize
// ledger. Retrying settles only the winners still pending.
func (s *CompetitionService) SettleWinners(ctx context.Context, admin AdminActor, id uuid.UUID, in SettleInput) ([]models.CompetitionWinner, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status != "finalised" {
		return nil, ErrCompetitionState
	}
	if in.Reference != nil {
		ref := strings.TrimSpace(*in.Reference)
		in.Reference = &ref
		if ref == "" {
			in.Reference = nil
		}
	}
	if _, err := s.repo.SettleWinners(ctx, id, in.Reference, admin.ledgerActor(),
		admin.audit("competition.winners_settled", id, in.Reference)); err != nil {
		switch {
		case errors.Is(err, repository.ErrCompetitionStateChanged):
			return nil, ErrCompetitionState
		case errors.Is(err, repository.ErrPrizeOutOfRange):
			return nil, ErrPrizeOutOfRange
		}
		return nil, err
	}
	return s.repo.Winners(ctx, id)
}

// Ledger returns every prize entry with a fresh reconciliation.
func (s *CompetitionService) Ledger(ctx context.Context, id uuid.UUID) (*models.PrizeLedgerView, error) {
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	entries, err := s.repo.Ledger(ctx, id)
	if err != nil {
		return nil, err
	}
	rec, err := s.repo.Reconcile(ctx, id)
	if err != nil {
		return nil, err
	}
	return &models.PrizeLedgerView{Entries: entries, Reconciliation: *rec}, nil
}

// AdminPlays lists a round's plays for the ledger and void tools.
func (s *CompetitionService) AdminPlays(ctx context.Context, id uuid.UUID, limit int) ([]models.AdminPlay, error) {
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	return s.repo.Plays(ctx, id, limit)
}

// Reconcile recomputes one round's prize from its ledger (AC-12).
func (s *CompetitionService) Reconcile(ctx context.Context, id uuid.UUID) (*models.Reconciliation, error) {
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.Reconcile(ctx, id)
}

// Analytics is the Super Admin dashboard for one round (spec §10).
func (s *CompetitionService) Analytics(ctx context.Context, id uuid.UUID) (*models.CompetitionAnalytics, error) {
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	a, err := s.repo.Analytics(ctx, id)
	if errors.Is(err, repository.ErrCompetitionNotFound) {
		return nil, ErrCompetitionNotFound
	}
	return a, err
}

// ReconcileAll checks every round whose money can still move. It is the
// scheduled reconciliation job; a discrepancy is logged and audited, never
// corrected automatically.
func (s *CompetitionService) ReconcileAll(ctx context.Context) {
	if err := s.repo.SyncStatuses(ctx); err != nil {
		log.Printf("prize reconciliation: sync statuses: %v", err)
	}
	if err := s.repo.PruneEvents(ctx, 7*24*time.Hour); err != nil {
		log.Printf("prize reconciliation: prune live events: %v", err)
	}
	ids, err := s.repo.ReconcilableIDs(ctx)
	if err != nil {
		log.Printf("prize reconciliation: list rounds: %v", err)
		return
	}
	for _, id := range ids {
		rec, err := s.repo.Reconcile(ctx, id)
		if err != nil {
			log.Printf("prize reconciliation: %s: %v", id, err)
			continue
		}
		if rec.Status != "ok" {
			log.Printf("prize reconciliation: DISCREPANCY in round %s: %+v", id, rec.Checks)
		}
	}
}

// RunReconciliation runs ReconcileAll on an interval until ctx ends.
func (s *CompetitionService) RunReconciliation(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ReconcileAll(ctx)
		}
	}
}

// DuplicateInput creates a new draft from an existing round's settings.
// NextRound makes it the following round of the same game — round_no + 1,
// linked to this one — which is how a game is "reset" without erasing the
// round before it (spec §2.1).
type DuplicateInput struct {
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Title     *string   `json:"title"`
	NextRound bool      `json:"next_round"`
}

// DuplicateCompetition copies a round's rules and economics into a new draft
// with a fresh seed. The prize reserve is not copied: every round must be
// funded on its own.
func (s *CompetitionService) DuplicateCompetition(ctx context.Context, admin AdminActor, id uuid.UUID, in DuplicateInput) (*models.AdminCompetitionView, error) {
	src, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	title := src.Title
	if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
		title = *in.Title
	}
	questions, err := s.repo.QuizQuestions(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	startPrize := src.StartPrizeCents
	requiresID := src.RequiresIdentityVerification
	continueAtCap := src.ContinueAtCap
	c := &models.Competition{}
	if err := s.apply(ctx, c, CompetitionInput{
		CountryID:                    src.CountryID.String(),
		GameSlug:                     src.GameSlug,
		Title:                        title,
		StartsAt:                     in.StartsAt,
		EndsAt:                       in.EndsAt,
		Timezone:                     src.Timezone,
		PointsPerAttempt:             src.PointsPerAttempt,
		MaxAttemptsPerCustomer:       src.MaxAttemptsPerCustomer,
		MinAge:                       src.MinAge,
		RequiresIdentityVerification: &requiresID,
		NumberOfWinners:              src.NumberOfWinners,
		PrizeDescription:             src.PrizeDescription,
		PrizeCurrency:                src.PrizeCurrency,
		OfficialRules:                src.OfficialRules,
		PrizeGrowthEnabled:           src.PrizeGrowthEnabled,
		PrizeType:                    src.PrizeType,
		WinnerMethod:                 src.WinnerMethod,
		StartPrizeCents:              &startPrize,
		IncrementPerPlayCents:        src.IncrementPerPlayCents,
		MaxPrizeCents:                src.MaxPrizeCents,
		ContinueAtCap:                &continueAtCap,
		DailyPlayLimit:               src.DailyPlayLimit,
		MinPlaysToWin:                src.MinPlaysToWin,
		WinOdds:                      src.WinOdds,
		QuizQuestions:                questions,
	}); err != nil {
		return nil, err
	}
	if in.NextRound {
		c.RoundNo = src.RoundNo + 1
		c.PreviousRoundID = &src.ID
	}
	seed, err := games.NewSeed()
	if err != nil {
		return nil, err
	}
	c.ServerSeed = seed
	adminID := admin.ID
	c.CreatedByAdminID = &adminID
	c.UpdatedByAdminID = &adminID
	action := "competition.duplicated"
	if in.NextRound {
		action = "competition.next_round_created"
	}
	audit := admin.audit(action, uuid.Nil, nil)
	audit.Before = map[string]any{"source_competition_id": src.ID}
	if err := s.repo.Create(ctx, c, audit); err != nil {
		return nil, err
	}
	return s.AdminGet(ctx, c.ID)
}

// ─── Prize draws ──────────────────────────────────────────────────────────

// RunDraw draws a closed prize-draw round's winners (spec §7 "Random
// outcomes"). Every play is one entry; entries are drawn at random without
// replacement with the secure generator, a customer can win once, and a
// drawn customer who no longer meets the rules is recorded as skipped — the
// draw moves on rather than re-drawing. The entry list, its hash, every
// random value and every pick are stored, and the round is finalised with
// its winners in the same transaction.
func (s *CompetitionService) RunDraw(ctx context.Context, admin AdminActor, id uuid.UUID) (*models.AdminCompetitionView, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.WinnerMethod != "draw" || (c.Status != "closed" && c.Status != "frozen") {
		return nil, fmt.Errorf("%w: only a closed prize-draw round can be drawn", ErrCompetitionState)
	}
	entries, err := s.repo.DrawEntries(ctx, id)
	if err != nil {
		return nil, err
	}

	var plays map[uuid.UUID]int
	if c.MinPlaysToWin != nil {
		ids := make([]uuid.UUID, 0, len(entries))
		for _, e := range entries {
			if cid, err := uuid.Parse(e.CustomerID); err == nil {
				ids = append(ids, cid)
			}
		}
		if plays, err = s.repo.PlayCounts(ctx, id, ids); err != nil {
			return nil, err
		}
	}
	cache := capabilityCache{}
	var lookupErr error
	eligible := func(customerID string) (bool, string) {
		cid, err := uuid.Parse(customerID)
		if err != nil {
			return false, "unknown customer"
		}
		cust, err := s.customers.GetByID(ctx, customerID)
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return false, "account closed"
		}
		if err != nil {
			lookupErr = err
			return false, "lookup failed"
		}
		ok, reason, err := s.eligibility(ctx, c, cust, cache)
		if err != nil {
			lookupErr = err
			return false, "lookup failed"
		}
		if ok && c.MinPlaysToWin != nil && plays[cid] < *c.MinPlaysToWin {
			return false, fmt.Sprintf("played %d of the %d plays needed to win", plays[cid], *c.MinPlaysToWin)
		}
		return ok, reason
	}
	picks, randoms, err := games.RunDraw(entries, c.NumberOfWinners, eligible)
	if err != nil {
		return nil, err
	}
	if lookupErr != nil {
		return nil, lookupErr
	}

	var winners []games.DrawPick
	for _, p := range picks {
		if p.Outcome == "winner" {
			winners = append(winners, p)
		}
	}
	total := c.CurrentPrizeCents
	if c.FinalPrizeCents != nil {
		total = *c.FinalPrizeCents
	}
	values := repository.SplitPrize(total, len(winners))
	plans := make([]repository.WinnerPlan, 0, len(winners))
	for i, w := range winners {
		cid, _ := uuid.Parse(w.CustomerID)
		playID, _ := uuid.Parse(w.PlayID)
		value := values[i]
		plans = append(plans, repository.WinnerPlan{
			CustomerID: cid, AttemptID: &playID, PrizePosition: i + 1, Rank: i + 1,
			Status: "pending_validation", PrizeValueCents: &value,
		})
	}

	adminID := admin.ID
	audit := admin.audit("competition.draw_run", id, nil)
	audit.After = map[string]any{"entries": len(entries), "winners": len(plans), "picks": picks}
	err = s.repo.ApplyDraw(ctx, id, repository.DrawRecord{
		Entries: entries, EntriesHash: games.DrawEntriesHash(entries), RandomValues: randoms,
		Picks: picks, AdminID: &adminID,
	}, plans, audit)
	if errors.Is(err, repository.ErrCompetitionStateChanged) {
		return nil, fmt.Errorf("%w: the round changed while drawing; try again", ErrCompetitionState)
	}
	if err != nil {
		return nil, err
	}
	return s.AdminGet(ctx, id)
}

// Draw returns a round's stored prize draw for the audit view.
func (s *CompetitionService) Draw(ctx context.Context, id uuid.UUID) (*repository.PrizeDraw, error) {
	if _, err := s.load(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.GetDraw(ctx, id)
}

// ─── Live prize stream ────────────────────────────────────────────────────

// LiveSnapshot is the first event on a live stream: the round as it is now,
// in the same shape as a PRIZE_UPDATED event.
type LiveSnapshot struct {
	Event             string    `json:"event"`
	GameID            uuid.UUID `json:"game_id"`
	Status            string    `json:"status"`
	CurrentPrizeCents int64     `json:"current_prize_cents"`
	EligiblePlayCount int64     `json:"eligible_play_count"`
	PrizeCapReached   bool      `json:"prize_cap_reached"`
	Version           int64     `json:"version"`
}

// LiveStart returns the round's current state and the outbox position a
// live stream should read from.
func (s *CompetitionService) LiveStart(ctx context.Context, id uuid.UUID) (*LiveSnapshot, int64, error) {
	c, err := s.load(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	if c.Status == "draft" {
		return nil, 0, ErrCompetitionNotFound
	}
	seq, err := s.repo.LatestEventSeq(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	return &LiveSnapshot{
		Event: "SNAPSHOT", GameID: c.ID, Status: s.status(c), CurrentPrizeCents: c.CurrentPrizeCents,
		EligiblePlayCount: c.EligiblePlayCount, PrizeCapReached: capReached(c), Version: c.PrizeVersion,
	}, seq, nil
}

// LiveEvents reads committed events after a stream position. The status
// clock is advanced first, so a round that just ended announces it.
func (s *CompetitionService) LiveEvents(ctx context.Context, id uuid.UUID, after int64) ([]models.PrizeEvent, error) {
	if err := s.repo.SyncStatuses(ctx); err != nil {
		return nil, err
	}
	return s.repo.EventsSince(ctx, id, after, 100)
}

// ─── Points ───────────────────────────────────────────────────────────────

var ErrInvalidPoints = errors.New("invalid points change")

// PointsService is the customer points wallet.
type PointsService struct {
	points *repository.PointsRepository
}

func NewPointsService(points *repository.PointsRepository) *PointsService {
	return &PointsService{points: points}
}

// MyWallet is the signed-in customer's balance and history.
func (s *PointsService) MyWallet(ctx context.Context, actor SocialActor) (*models.PointsWallet, error) {
	id, err := parseCustomer(actor)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return nil, ErrCustomerRequired
	}
	return s.points.Wallet(ctx, *id, 100)
}

// CustomerWallet is any customer's wallet, for admins.
func (s *PointsService) CustomerWallet(ctx context.Context, customerID uuid.UUID) (*models.PointsWallet, error) {
	return s.points.Wallet(ctx, customerID, 200)
}

// SearchCustomers finds customers for the admin points tools.
func (s *PointsService) SearchCustomers(ctx context.Context, query string) ([]models.CustomerPointsSummary, error) {
	query = strings.TrimSpace(query)
	if len(query) > 100 {
		query = query[:100]
	}
	// LIKE wildcards in the query are matched literally.
	query = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query)
	return s.points.SearchCustomers(ctx, query, 50)
}

// AdjustPointsInput is a Super Admin granting or taking points. The key
// makes a retried request harmless.
type AdjustPointsInput struct {
	Amount         int64  `json:"amount"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

// AdjustPoints grants (positive) or deducts (negative) a customer's points
// with a mandatory reason, as a ledger entry with its audit row.
func (s *PointsService) AdjustPoints(ctx context.Context, admin AdminActor, customerID uuid.UUID, in AdjustPointsInput) (*models.PointsWallet, error) {
	reason := strings.TrimSpace(in.Reason)
	key := strings.TrimSpace(in.IdempotencyKey)
	switch {
	case in.Amount == 0:
		return nil, fmt.Errorf("%w: amount cannot be zero", ErrInvalidPoints)
	case in.Amount > 10_000_000 || in.Amount < -10_000_000:
		return nil, fmt.Errorf("%w: amount is out of range", ErrInvalidPoints)
	case reason == "":
		return nil, fmt.Errorf("%w: a reason is required", ErrInvalidPoints)
	case key == "" || len(key) > 128:
		return nil, fmt.Errorf("%w: idempotency_key of 1 to 128 characters is required", ErrInvalidPoints)
	}
	entryType := models.PointsEntryAdminGrant
	if in.Amount < 0 {
		entryType = models.PointsEntryAdminDeduction
	}
	adminID := admin.ID
	audit := admin.audit("points.adjusted", customerID, &reason)
	audit.EntityType = "customer"
	_, err := s.points.Adjust(ctx, repository.PointsChange{
		CustomerID: customerID, EntryType: entryType, Delta: in.Amount,
		IdempotencyKey: "admin:" + key, Reason: &reason, ActorType: "admin", ActorID: &adminID,
	}, audit)
	switch {
	case errors.Is(err, repository.ErrPointsInsufficient):
		return nil, fmt.Errorf("%w: the customer does not have that many points", ErrInvalidPoints)
	case errors.Is(err, repository.ErrPointsDuplicate):
		return nil, fmt.Errorf("%w: this idempotency_key was already used", ErrConfigConflict)
	case isForeignKeyViolation(err):
		return nil, fmt.Errorf("%w: customer not found", ErrInvalidPoints)
	case err != nil:
		return nil, err
	}
	return s.points.Wallet(ctx, customerID, 200)
}

// ─── Points earning ───────────────────────────────────────────────────────

// EarningRules lists every country's earning rule.
func (s *PointsService) EarningRules(ctx context.Context) ([]models.PointsEarningRule, error) {
	return s.points.EarningRules(ctx)
}

// MyEarningRule is how the signed-in customer earns points.
func (s *PointsService) MyEarningRule(ctx context.Context, actor SocialActor) (*models.PointsEarningRule, error) {
	id, err := parseCustomer(actor)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return nil, ErrCustomerRequired
	}
	return s.points.RuleForCustomer(ctx, *id)
}

// EarningRuleInput is a Super Admin setting one country's rule.
type EarningRuleInput struct {
	Enabled       bool `json:"enabled"`
	PointsPerUnit int  `json:"points_per_unit"`
	SignupBonus   int  `json:"signup_bonus"`
}

// SetEarningRule saves a country's earning rule, audited.
func (s *PointsService) SetEarningRule(ctx context.Context, admin AdminActor, countryID uuid.UUID, in EarningRuleInput) ([]models.PointsEarningRule, error) {
	if in.PointsPerUnit < 0 || in.PointsPerUnit > 10000 {
		return nil, fmt.Errorf("%w: points_per_unit must be 0 to 10000", ErrInvalidPoints)
	}
	if in.SignupBonus < 0 || in.SignupBonus > 1000000 {
		return nil, fmt.Errorf("%w: signup_bonus must be 0 to 1000000", ErrInvalidPoints)
	}
	audit := admin.audit("points.earning_rule_set", countryID, nil)
	audit.EntityType = "country"
	audit.After = in
	err := s.points.SetEarningRule(ctx, countryID, in.Enabled, in.PointsPerUnit, in.SignupBonus, admin.ID, audit)
	if errors.Is(err, repository.ErrCountryNotFound) {
		return nil, fmt.Errorf("%w: country not found", ErrInvalidPoints)
	}
	if err != nil {
		return nil, err
	}
	return s.points.EarningRules(ctx)
}

// RunEarning runs one pass of the earning job now.
func (s *PointsService) RunEarning(ctx context.Context) (*models.EarningRunResult, error) {
	return s.points.RunEarning(ctx, 500)
}

// RunEarningLoop runs the earning job on an interval until ctx ends.
func (s *PointsService) RunEarningLoop(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, err := s.points.RunEarning(ctx, 500)
			if err != nil {
				log.Printf("points earning: %v", err)
				continue
			}
			if res.OrdersRewarded+res.OrdersReversed+res.SignupBonuses > 0 {
				log.Printf("points earning: %+v", *res)
			}
		}
	}
}
