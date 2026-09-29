package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"myapp/internal/games"
	"myapp/internal/models"
)

// The progressive prize engine's storage (Progressive Prize spec §3–§4).
//
// One rule runs through every function here: anything that moves money —
// points or prize — first locks the round's competition row. That makes all
// changes to one round happen one at a time, so no increment is lost under
// concurrency, the cap cannot be overshot, and every ledger balance is
// computed from the one before it.

var (
	ErrRoundNotLive       = errors.New("round is not accepting plays")
	ErrRoundPaused        = errors.New("round is paused")
	ErrRoundNotStarted    = errors.New("round has not started")
	ErrRoundOutsideWindow = errors.New("round is outside its play window")
	ErrPlayKeyConflict    = errors.New("idempotency key was already used for a different play")
	ErrPrizeCapReached    = errors.New("the prize has reached its maximum and this round stops at the cap")
	ErrPrizeOutOfRange    = errors.New("that would take the prize below zero or above its maximum")
	ErrPlayNotFound       = errors.New("play not found")
	ErrPlayState          = errors.New("play is not in the right state for this action")
)

// PlayLimitError is a play refused by a per-customer limit.
type PlayLimitError struct {
	Kind           string // "round" or "daily"
	Limit          int
	NextEligibleAt *time.Time
}

func (e *PlayLimitError) Error() string {
	if e.Kind == "daily" {
		return fmt.Sprintf("daily play limit of %d reached", e.Limit)
	}
	return fmt.Sprintf("play limit of %d reached for this round", e.Limit)
}

// InsufficientPointsError is a play refused for lack of points. Nothing was
// charged.
type InsufficientPointsError struct {
	Required int64
	Balance  int64
}

func (e *InsufficientPointsError) Error() string {
	return fmt.Sprintf("this play costs %d points and the balance is %d", e.Required, e.Balance)
}

// Actor is who caused a ledger entry.
type Actor struct {
	Type string // admin, system or customer
	ID   *uuid.UUID
}

// ─── The locked round ─────────────────────────────────────────────────────

// lockedRound is a competition read under its row lock, with the database
// clock at that moment. Its prize fields are updated as entries are posted
// in the same transaction.
type lockedRound struct {
	ID               uuid.UUID
	Status           string
	StartsAt         time.Time
	EndsAt           time.Time
	Timezone         string
	PointsPerAttempt int
	MaxAttempts      int
	DailyLimit       *int
	Growth           bool
	Increment        int64
	Max              *int64
	ContinueAtCap    bool
	StartPrize       int64
	Current          int64
	Plays            int64
	Version          int64
	GameVersionID    uuid.UUID
	Seed             string
	Config           []byte
	Now              time.Time
	GameSlug         string
	WinnerMethod     string
	WinOdds          *int
	Winners          int
}

func lockRound(ctx context.Context, q querier, id uuid.UUID) (*lockedRound, error) {
	var r lockedRound
	err := q.QueryRow(ctx, `
		select c.id, c.status, c.starts_at, c.ends_at, c.timezone, c.points_per_attempt,
		       c.max_attempts_per_customer, c.daily_play_limit, c.prize_growth_enabled,
		       c.increment_per_play_cents, c.max_prize_cents, c.continue_at_cap, c.start_prize_cents,
		       c.current_prize_cents, c.eligible_play_count, c.prize_version,
		       c.game_version_id, c.server_seed, v.config, now(),
		       g.slug, c.winner_method, c.win_odds, c.number_of_winners
		from competition.competitions c
		inner join competition.game_versions v on v.id = c.game_version_id
		inner join competition.games g on g.id = v.game_id
		where c.id = $1
		for update of c`, id).
		Scan(&r.ID, &r.Status, &r.StartsAt, &r.EndsAt, &r.Timezone, &r.PointsPerAttempt,
			&r.MaxAttempts, &r.DailyLimit, &r.Growth, &r.Increment, &r.Max, &r.ContinueAtCap,
			&r.StartPrize, &r.Current, &r.Plays, &r.Version, &r.GameVersionID, &r.Seed, &r.Config, &r.Now,
			&r.GameSlug, &r.WinnerMethod, &r.WinOdds, &r.Winners)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCompetitionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// insertOutbox records an event in the same transaction as its change.
func insertOutbox(ctx context.Context, q querier, competitionID uuid.UUID, eventType string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		insert into core.outbox_events (aggregate_type, aggregate_id, event_type, payload)
		values ('competition', $1, $2, $3)`, competitionID, eventType, raw)
	return err
}

// prizeMove is one change to a round's prize and play counters.
type prizeMove struct {
	EntryType string
	Delta     int64
	AttemptID *uuid.UUID
	WinnerID  *uuid.UUID
	Reason    *string
	Actor     Actor
	// Plays and NewPlayers adjust the cached counters alongside the prize.
	Plays      int64
	NewPlayers int64
}

// postPrize writes one prize movement: a ledger row (unless nothing moved),
// the cached prize and counters, and a PRIZE_UPDATED event. The prize may
// never leave [0, max]; the caller holds the round lock.
func postPrize(ctx context.Context, q querier, r *lockedRound, m prizeMove) (*models.PrizeLedgerEntry, error) {
	after := r.Current + m.Delta
	if after < 0 || (m.Delta > 0 && r.Max != nil && after > *r.Max) {
		return nil, ErrPrizeOutOfRange
	}

	var entry *models.PrizeLedgerEntry
	if m.Delta != 0 || m.EntryType == models.PrizeEntrySeed {
		e := models.PrizeLedgerEntry{
			CompetitionID: r.ID, AttemptID: m.AttemptID, WinnerID: m.WinnerID, EntryType: m.EntryType,
			AmountDeltaCents: m.Delta, BalanceAfterCents: after, Reason: m.Reason,
			ActorType: m.Actor.Type, ActorID: m.Actor.ID,
		}
		err := q.QueryRow(ctx, `
			insert into competition.prize_ledger
				(competition_id, attempt_id, winner_id, entry_type, amount_delta_cents, balance_after_cents,
				 reason, actor_type, actor_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			returning id, seq, created_at`,
			r.ID, m.AttemptID, m.WinnerID, m.EntryType, m.Delta, after, m.Reason, m.Actor.Type, m.Actor.ID).
			Scan(&e.ID, &e.Seq, &e.CreatedAt)
		if err != nil {
			return nil, err
		}
		entry = &e
	}

	if err := q.QueryRow(ctx, `
		update competition.competitions
		set current_prize_cents = $2,
		    eligible_play_count = eligible_play_count + $3,
		    unique_player_count = unique_player_count + $4,
		    prize_version = prize_version + 1,
		    updated_at = now()
		where id = $1
		returning eligible_play_count, prize_version`, r.ID, after, m.Plays, m.NewPlayers).
		Scan(&r.Plays, &r.Version); err != nil {
		return nil, err
	}
	r.Current = after

	return entry, insertOutbox(ctx, q, r.ID, "PRIZE_UPDATED", map[string]any{
		"event":               "PRIZE_UPDATED",
		"game_id":             r.ID,
		"current_prize_cents": r.Current,
		"eligible_play_count": r.Plays,
		"version":             r.Version,
	})
}

// seedRound posts the round's starting prize when it is published: the SEED
// entry the first time, or a correction if the start prize was edited
// before the round opened.
func seedRound(ctx context.Context, q querier, r *lockedRound, actor Actor) error {
	var seeded bool
	if err := q.QueryRow(ctx, `
		select exists (select 1 from competition.prize_ledger
		               where competition_id = $1 and entry_type = 'seed')`, r.ID).Scan(&seeded); err != nil {
		return err
	}
	if !seeded {
		reason := "starting prize"
		_, err := postPrize(ctx, q, r, prizeMove{
			EntryType: models.PrizeEntrySeed, Delta: r.StartPrize - r.Current, Reason: &reason, Actor: actor,
		})
		return err
	}
	if delta := r.StartPrize - r.Current; delta != 0 {
		reason := "starting prize changed before the round opened"
		_, err := postPrize(ctx, q, r, prizeMove{
			EntryType: models.PrizeEntryCorrection, Delta: delta, Reason: &reason, Actor: actor,
		})
		return err
	}
	return nil
}

// ─── Plays ────────────────────────────────────────────────────────────────

// PlayInput is one customer asking to play a round.
type PlayInput struct {
	CompetitionID   uuid.UUID
	CustomerID      uuid.UUID
	ClientRequestID string
	SessionTTL      time.Duration
	RiskMetadata    map[string]any
}

// PlayOutcome is a committed play, or the original one when the request was
// a retry.
type PlayOutcome struct {
	Attempt       models.CompetitionAttempt
	Session       models.GameSession
	Replayed      bool
	PointsBalance int64
	AttemptsUsed  int
	CapReached    bool
}

// StartPlay is the atomic play transaction (spec §4.1). In order, under the
// round's row lock:
//
//  1. a retry of an earlier request returns that play, charging nothing;
//  2. the round must be live and inside its window, by the database clock;
//  3. the customer's round and daily limits;
//  4. the points balance, locked;
//  5. the increment, cut to the cap;
//  6. debit, session, play, prize entry, cached prize and event, all at
//     once.
//
// Any failure rolls the whole thing back: no points move and the prize does
// not change.
func (r *CompetitionRepository) StartPlay(ctx context.Context, in PlayInput) (*PlayOutcome, error) {
	var out *PlayOutcome
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		round, err := lockRound(ctx, tx, in.CompetitionID)
		if err != nil {
			return err
		}

		// 1. Idempotency: the same key from the same customer is the same play.
		var prior struct {
			ID            uuid.UUID
			CompetitionID uuid.UUID
		}
		err = tx.QueryRow(ctx, `
			select id, competition_id from competition.competition_attempts
			where customer_id = $1 and client_request_id = $2`, in.CustomerID, in.ClientRequestID).
			Scan(&prior.ID, &prior.CompetitionID)
		switch {
		case err == nil:
			if prior.CompetitionID != in.CompetitionID {
				return ErrPlayKeyConflict
			}
			out, err = loadPlayOutcome(ctx, tx, prior.ID, in.CustomerID)
			if err == nil {
				out.Replayed = true
			}
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}

		// 2. The round must be open right now.
		switch round.Status {
		case "live", "scheduled":
		case "paused":
			return ErrRoundPaused
		case "draft":
			return ErrCompetitionNotFound
		default:
			return ErrRoundNotLive
		}
		if round.Now.Before(round.StartsAt) {
			return ErrRoundNotStarted
		}
		if !round.Now.Before(round.EndsAt) {
			return ErrRoundOutsideWindow
		}

		// 3. Limits.
		dayStart, dayNext := DayWindow(round.Now, round.Timezone)
		var active, total, today int
		if err := tx.QueryRow(ctx, `
			select count(*) filter (where status <> 'voided'),
			       count(*),
			       count(*) filter (where status <> 'voided' and started_at >= $3)
			from competition.competition_attempts
			where competition_id = $1 and customer_id = $2`,
			in.CompetitionID, in.CustomerID, dayStart).Scan(&active, &total, &today); err != nil {
			return err
		}
		if active >= round.MaxAttempts {
			return &PlayLimitError{Kind: "round", Limit: round.MaxAttempts}
		}
		if round.DailyLimit != nil && today >= *round.DailyLimit {
			next := dayNext
			if !next.Before(round.EndsAt) {
				return &PlayLimitError{Kind: "daily", Limit: *round.DailyLimit}
			}
			return &PlayLimitError{Kind: "daily", Limit: *round.DailyLimit, NextEligibleAt: &next}
		}

		// 4. Points.
		cost := int64(round.PointsPerAttempt)
		balance, err := lockPointsAccount(ctx, tx, in.CustomerID)
		if err != nil {
			return err
		}
		if balance < cost {
			return &InsufficientPointsError{Required: cost, Balance: balance}
		}

		// 5. The increment.
		increment, atCap := PlayIncrement(round.Growth, round.Increment, round.Max, round.Current)
		if atCap && round.Growth && !round.ContinueAtCap {
			return ErrPrizeCapReached
		}

		// 6. Write it all.
		attemptID := uuid.New()
		var ledgerID *uuid.UUID
		if cost > 0 {
			id := uuid.New()
			ledgerID = &id
			if _, balance, err = applyPoints(ctx, tx, PointsChange{
				CustomerID: in.CustomerID, EntryType: models.PointsEntryPlayDebit, Delta: -cost,
				CompetitionID: &in.CompetitionID, AttemptID: &attemptID,
				IdempotencyKey: "play:" + attemptID.String(),
				ActorType:      "customer", ActorID: &in.CustomerID, EntryID: &id,
			}); err != nil {
				return err
			}
		}

		// A quiz play gets this round's questions without their answers.
		if round.GameSlug == games.QuizSlug {
			qs, err := loadQuiz(ctx, tx, in.CompetitionID)
			if err != nil {
				return err
			}
			if round.Config, err = games.PublicQuizConfig(qs, int(in.SessionTTL.Seconds())); err != nil {
				return err
			}
		}

		expires := round.Now.Add(in.SessionTTL)
		if expires.After(round.EndsAt) {
			expires = round.EndsAt
		}
		config := round.Config
		if len(config) == 0 {
			config = []byte("{}")
		}
		var session models.GameSession
		var rawConfig []byte
		if err := tx.QueryRow(ctx, `
			insert into competition.game_sessions
				(game_version_id, customer_id, mode, competition_id, server_seed, config, started_at, expires_at)
			values ($1, $2, 'official', $3, $4, $5, $6, $7)
			returning id, game_version_id, customer_id, guest_token, mode, competition_id,
			          server_seed, config, status, started_at, expires_at, submitted_at`,
			round.GameVersionID, in.CustomerID, in.CompetitionID, round.Seed, config, round.Now, expires).
			Scan(&session.ID, &session.GameVersionID, &session.CustomerID, &session.GuestToken, &session.Mode,
				&session.CompetitionID, &session.ServerSeed, &rawConfig, &session.Status, &session.StartedAt,
				&session.ExpiresAt, &session.SubmittedAt); err != nil {
			return err
		}
		session.Config = json.RawMessage(rawConfig)

		risk, err := json.Marshal(in.RiskMetadata)
		if err != nil || in.RiskMetadata == nil {
			risk = []byte("{}")
		}
		before, after := round.Current, round.Current+increment
		number := total + 1
		var attempt models.CompetitionAttempt
		err = tx.QueryRow(ctx, `
			insert into competition.competition_attempts
				(id, competition_id, customer_id, session_id, points_ledger_id, points_spent,
				 attempt_number, server_seed, idempotency_key, client_request_id,
				 prize_increment_cents, prize_before_cents, prize_after_cents, risk_metadata, started_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			returning `+attemptColumns,
			attemptID, in.CompetitionID, in.CustomerID, session.ID, ledgerID, cost,
			number, round.Seed, fmt.Sprintf("%s:%s:%d", in.CompetitionID, in.CustomerID, number),
			in.ClientRequestID, increment, before, after, risk, round.Now).
			Scan(attemptDest(&attempt)...)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == "competition_attempts_client_request_uq" {
				return ErrPlayKeyConflict
			}
			return ErrAttemptConflict
		}
		if err != nil {
			return err
		}

		newPlayers := int64(0)
		if total == 0 {
			newPlayers = 1
		}
		entryType := models.PrizeEntryPlayIncrement
		if increment == 0 {
			entryType = "" // nothing to post; counters still move
		}
		if _, err := postPrize(ctx, tx, round, prizeMove{
			EntryType: entryType, Delta: increment, AttemptID: &attemptID,
			Actor: Actor{Type: "customer", ID: &in.CustomerID}, Plays: 1, NewPlayers: newPlayers,
		}); err != nil {
			return err
		}
		if mechanic, ok := games.ChanceMechanic(round.GameSlug); ok {
			if err := resolveChance(ctx, tx, round, mechanic, &attempt, session.ID); err != nil {
				return err
			}
		}
		if err := insertOutbox(ctx, tx, in.CompetitionID, "GAME_PLAY_COMPLETED", map[string]any{
			"event": "GAME_PLAY_COMPLETED", "game_id": in.CompetitionID, "play_id": attemptID,
			"points_spent": cost, "prize_increment_cents": increment,
		}); err != nil {
			return err
		}

		_, capNow := PlayIncrement(round.Growth, round.Increment, round.Max, round.Current)
		out = &PlayOutcome{
			Attempt: attempt, Session: session, PointsBalance: balance,
			AttemptsUsed: active + 1, CapReached: round.Max != nil && capNow,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const attemptColumns = `id, competition_id, customer_id, session_id, points_ledger_id, points_spent,
	attempt_number, status, void_reason, started_at, submitted_at,
	client_request_id, prize_increment_cents, prize_before_cents, prize_after_cents, refunded_at,
	result_payload`

func attemptDest(a *models.CompetitionAttempt) []any {
	return []any{&a.ID, &a.CompetitionID, &a.CustomerID, &a.SessionID, &a.PointsLedgerID, &a.PointsSpent,
		&a.AttemptNumber, &a.Status, &a.VoidReason, &a.StartedAt, &a.SubmittedAt,
		&a.ClientRequestID, &a.PrizeIncrementCents, &a.PrizeBeforeCents, &a.PrizeAfterCents, &a.RefundedAt,
		&a.ResultPayload}
}

// resolveChance settles a chance play inside the play transaction: an
// instant round draws the outcome now, a prize draw records the entry. There
// is nothing to submit afterwards, so the session closes straight away. An
// instant win records the winner at the prize the play left it on and closes
// the round, so under the round lock there can never be a second winner.
func resolveChance(ctx context.Context, q querier, r *lockedRound, mechanic string, a *models.CompetitionAttempt, sessionID uuid.UUID) error {
	var result any
	won := false
	if mechanic == games.MechanicDraw || r.WinnerMethod == "draw" {
		result = map[string]any{"mechanic": games.MechanicDraw, "entered": true, "entry_number": r.Plays}
	} else {
		if r.WinOdds == nil {
			return fmt.Errorf("instant round %s has no win odds", r.ID)
		}
		res, err := games.DrawInstant(mechanic, int64(*r.WinOdds), r.Config)
		if err != nil {
			return err
		}
		result, won = res, res.Won
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		update competition.game_sessions set status = 'submitted', submitted_at = now(), updated_at = now()
		where id = $1`, sessionID); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		update competition.competition_attempts
		set status = 'accepted', result_payload = $2, submitted_at = now(), updated_at = now()
		where id = $1`, a.ID, raw); err != nil {
		return err
	}
	a.Status, a.ResultPayload = "accepted", raw
	if !won {
		return nil
	}

	prize := r.Current
	if err := insertWinner(ctx, q, r.ID, WinnerPlan{
		CustomerID: a.CustomerID, AttemptID: &a.ID, PrizePosition: 1, Rank: 1,
		Status: "pending_validation", PrizeValueCents: &prize,
	}); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		update competition.competitions
		set status = 'closed', closed_at = now(), final_prize_cents = current_prize_cents,
		    prize_version = prize_version + 1, updated_at = now()
		where id = $1`, r.ID); err != nil {
		return err
	}
	r.Status, r.Version = "closed", r.Version+1
	return insertOutbox(ctx, q, r.ID, "STATUS_CHANGED", map[string]any{
		"event": "STATUS_CHANGED", "game_id": r.ID, "status": "closed", "reason": "instant_win",
		"current_prize_cents": r.Current, "eligible_play_count": r.Plays, "version": r.Version,
	})
}

// loadPlayOutcome rebuilds the receipt of an existing play, for a retried
// request.
func loadPlayOutcome(ctx context.Context, q querier, attemptID, customerID uuid.UUID) (*PlayOutcome, error) {
	out := &PlayOutcome{}
	if err := q.QueryRow(ctx, `select `+attemptColumns+` from competition.competition_attempts where id = $1`,
		attemptID).Scan(attemptDest(&out.Attempt)...); err != nil {
		return nil, err
	}
	var rawConfig []byte
	s := &out.Session
	if err := q.QueryRow(ctx, `
		select id, game_version_id, customer_id, guest_token, mode, competition_id,
		       server_seed, config, status, started_at, expires_at, submitted_at
		from competition.game_sessions where id = $1`, out.Attempt.SessionID).
		Scan(&s.ID, &s.GameVersionID, &s.CustomerID, &s.GuestToken, &s.Mode, &s.CompetitionID,
			&s.ServerSeed, &rawConfig, &s.Status, &s.StartedAt, &s.ExpiresAt, &s.SubmittedAt); err != nil {
		return nil, err
	}
	s.Config = json.RawMessage(rawConfig)
	if err := q.QueryRow(ctx, `
		select coalesce((select balance from finance.points_accounts where customer_id = $1), 0),
		       (select count(*) from competition.competition_attempts
		         where competition_id = $2 and customer_id = $1 and status <> 'voided')`,
		customerID, out.Attempt.CompetitionID).Scan(&out.PointsBalance, &out.AttemptsUsed); err != nil {
		return nil, err
	}
	return out, nil
}

// RecordRejection notes a refused play for the analytics. Best effort: the
// refusal has already been decided.
func (r *CompetitionRepository) RecordRejection(ctx context.Context, competitionID uuid.UUID, customerID *uuid.UUID, code string) {
	_, _ = r.db.Exec(ctx, `
		insert into competition.play_rejections (competition_id, customer_id, reason_code)
		select $1, $2, $3 where exists (select 1 from competition.competitions where id = $1)`,
		competitionID, customerID, code)
}

// RecordView notes a signed-in customer opening a round. Best effort.
func (r *CompetitionRepository) RecordView(ctx context.Context, competitionID, customerID uuid.UUID) {
	_, _ = r.db.Exec(ctx, `
		insert into competition.competition_views (competition_id, customer_id)
		values ($1, $2)
		on conflict (competition_id, customer_id) do update
		set last_viewed_at = now(), view_count = competition.competition_views.view_count + 1`,
		competitionID, customerID)
}

// DailyPlays counts a customer's plays in a round since a moment.
func (r *CompetitionRepository) DailyPlays(ctx context.Context, competitionID, customerID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		select count(*) from competition.competition_attempts
		where competition_id = $1 and customer_id = $2 and status <> 'voided' and started_at >= $3`,
		competitionID, customerID, since).Scan(&n)
	return n, err
}

// saveQuiz replaces a round's quiz questions. Rounds of other games have
// none, so saving an empty list clears any left from an earlier edit.
func saveQuiz(ctx context.Context, q querier, id uuid.UUID, qs []games.QuizQuestion) error {
	if _, err := q.Exec(ctx, `delete from competition.quiz_questions where competition_id = $1`, id); err != nil {
		return err
	}
	for i, question := range qs {
		options, err := json.Marshal(question.Options)
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `
			insert into competition.quiz_questions
				(competition_id, position, prompt, options, correct_index, time_limit_seconds)
			values ($1, $2, $3, $4, $5, $6)`,
			id, i, question.Prompt, options, question.CorrectIndex, question.TimeLimitSeconds); err != nil {
			return err
		}
	}
	return nil
}

func loadQuiz(ctx context.Context, q querier, id uuid.UUID) ([]games.QuizQuestion, error) {
	rows, err := q.Query(ctx, `
		select prompt, options, correct_index, time_limit_seconds
		from competition.quiz_questions where competition_id = $1 order by position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]games.QuizQuestion, 0)
	for rows.Next() {
		var question games.QuizQuestion
		var options []byte
		if err := rows.Scan(&question.Prompt, &options, &question.CorrectIndex, &question.TimeLimitSeconds); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(options, &question.Options); err != nil {
			return nil, err
		}
		out = append(out, question)
	}
	return out, rows.Err()
}

// QuizQuestions returns a round's questions with their answers — for the
// server's scoring and for admins, never for players.
func (r *CompetitionRepository) QuizQuestions(ctx context.Context, id uuid.UUID) ([]games.QuizQuestion, error) {
	return loadQuiz(ctx, r.db, id)
}

// PlayCounts is how many plays each customer has in a round, voided plays
// excluded.
func (r *CompetitionRepository) PlayCounts(ctx context.Context, competitionID uuid.UUID, customerIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	out := make(map[uuid.UUID]int, len(customerIDs))
	if len(customerIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select customer_id, count(*) from competition.competition_attempts
		where competition_id = $1 and customer_id = any($2) and status <> 'voided'
		group by customer_id`, competitionID, customerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ─── Lifecycle ────────────────────────────────────────────────────────────

// setStatus moves a round between lifecycle states under its lock, with the
// audit row and a STATUS_CHANGED event.
func (r *CompetitionRepository) setStatus(ctx context.Context, id uuid.UUID, from []string, to string, extra string, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		round, err := lockRound(ctx, tx, id)
		if err != nil {
			return err
		}
		ok := false
		for _, f := range from {
			ok = ok || round.Status == f
		}
		if !ok || (to != "closed" && !round.Now.Before(round.EndsAt)) {
			return ErrCompetitionStateChanged
		}
		if _, err := tx.Exec(ctx, `
			update competition.competitions
			set status = $2, prize_version = prize_version + 1, updated_at = now()`+extra+`
			where id = $1`, id, to); err != nil {
			return err
		}
		audit.Before = map[string]any{"status": round.Status}
		audit.After = map[string]any{"status": to}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		return insertOutbox(ctx, tx, id, "STATUS_CHANGED", map[string]any{
			"event": "STATUS_CHANGED", "game_id": id, "status": to,
			"current_prize_cents": round.Current, "eligible_play_count": round.Plays,
			"version": round.Version + 1,
		})
	})
}

// Pause stops new plays. A play that already holds the round lock finishes
// first; any play that arrives after the pause commits is refused.
func (r *CompetitionRepository) Pause(ctx context.Context, id uuid.UUID, audit models.AuditEntry) error {
	return r.setStatus(ctx, id, []string{"live"}, "paused", ", paused_at = now()", audit)
}

// Resume reopens a paused round, if its window has not ended.
func (r *CompetitionRepository) Resume(ctx context.Context, id uuid.UUID, audit models.AuditEntry) error {
	return r.setStatus(ctx, id, []string{"paused"}, "live", ", paused_at = null", audit)
}

// Close ends a running round early, recording the prize it closed on.
func (r *CompetitionRepository) Close(ctx context.Context, id uuid.UUID, audit models.AuditEntry) error {
	return r.setStatus(ctx, id, []string{"live", "paused"}, "closed",
		", closed_at = now(), final_prize_cents = coalesce(final_prize_cents, current_prize_cents)", audit)
}

// ─── Admin money movements ────────────────────────────────────────────────

// AdjustPrize posts a Super Admin's change to the prize, with its reason, as
// a new ledger entry (spec §2.1). Allowed from publication until the round
// is finalised.
func (r *CompetitionRepository) AdjustPrize(ctx context.Context, id uuid.UUID, delta int64, reason string, actor Actor, audit models.AuditEntry) (*models.PrizeLedgerEntry, error) {
	var entry *models.PrizeLedgerEntry
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		round, err := lockRound(ctx, tx, id)
		if err != nil {
			return err
		}
		switch round.Status {
		case "scheduled", "live", "paused", "closed", "frozen":
		default:
			return ErrCompetitionStateChanged
		}
		before := round.Current
		if entry, err = postPrize(ctx, tx, round, prizeMove{
			EntryType: models.PrizeEntryAdminAdjustment, Delta: delta, Reason: &reason, Actor: actor,
		}); err != nil {
			return err
		}
		if round.Status == "closed" || round.Status == "frozen" {
			if _, err := tx.Exec(ctx, `
				update competition.competitions set final_prize_cents = current_prize_cents where id = $1`,
				id); err != nil {
				return err
			}
		}
		audit.Before = map[string]any{"current_prize_cents": before}
		audit.After = map[string]any{"current_prize_cents": round.Current, "ledger_entry_id": entry.ID}
		return insertAudit(ctx, tx, audit)
	})
	return entry, err
}

// VoidPlay voids one play: its score leaves the board, its points go back
// when refund is set, and its increment comes off the prize when reverse is
// set (spec §8). Every part is its own ledger entry.
func (r *CompetitionRepository) VoidPlay(ctx context.Context, id, attemptID uuid.UUID, reason string, refund, reverse bool, actor Actor, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		round, err := lockRound(ctx, tx, id)
		if err != nil {
			return err
		}
		switch round.Status {
		case "live", "paused", "closed", "frozen":
		default:
			return ErrCompetitionStateChanged
		}
		var a models.CompetitionAttempt
		err = tx.QueryRow(ctx, `select `+attemptColumns+`
			from competition.competition_attempts where id = $1 and competition_id = $2 for update`,
			attemptID, id).Scan(attemptDest(&a)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPlayNotFound
		}
		if err != nil {
			return err
		}
		if a.Status == "voided" {
			return ErrPlayState
		}
		if _, err := tx.Exec(ctx, `
			update competition.competition_attempts
			set status = 'voided', void_reason = $2, updated_at = now()
			where id = $1`, attemptID, reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update competition.score_submissions
			set validation_status = 'rejected', review_reason = $2,
			    reviewed_by_admin_id = $3, reviewed_at = now()
			where attempt_id = $1 and validation_status <> 'rejected'`, attemptID, "play voided: "+reason, actor.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update competition.game_sessions set status = 'expired', updated_at = now()
			where id = $1 and status = 'active'`, a.SessionID); err != nil {
			return err
		}
		if refund && a.PointsSpent > 0 && a.RefundedAt == nil {
			if err := refundPlay(ctx, tx, a, reason, actor); err != nil {
				return err
			}
		}
		delta := int64(0)
		entryType := ""
		if reverse && a.PrizeIncrementCents > 0 {
			delta, entryType = -a.PrizeIncrementCents, models.PrizeEntryPlayReversal
		}
		if _, err := postPrize(ctx, tx, round, prizeMove{
			EntryType: entryType, Delta: delta, AttemptID: &attemptID, Reason: &reason, Actor: actor, Plays: -1,
		}); err != nil {
			return err
		}
		audit.After = map[string]any{
			"play_id": attemptID, "refund_points": refund, "reverse_increment": reverse,
			"points_spent": a.PointsSpent, "prize_increment_cents": a.PrizeIncrementCents,
		}
		return insertAudit(ctx, tx, audit)
	})
}

func refundPlay(ctx context.Context, q querier, a models.CompetitionAttempt, reason string, actor Actor) error {
	if _, _, err := applyPoints(ctx, q, PointsChange{
		CustomerID: a.CustomerID, EntryType: models.PointsEntryPlayRefund, Delta: int64(a.PointsSpent),
		CompetitionID: &a.CompetitionID, AttemptID: &a.ID, IdempotencyKey: "refund:" + a.ID.String(),
		Reason: &reason, ActorType: actor.Type, ActorID: actor.ID,
	}); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `update competition.competition_attempts set refunded_at = now() where id = $1`, a.ID)
	return err
}

// refundRound gives back the points of every play that has not been
// refunded yet and withdraws the prize, for a cancelled round.
func refundRound(ctx context.Context, q querier, id uuid.UUID, reason string, actor Actor) error {
	round, err := lockRound(ctx, q, id)
	if err != nil {
		return err
	}
	rows, err := q.Query(ctx, `select `+attemptColumns+`
		from competition.competition_attempts
		where competition_id = $1 and points_spent > 0 and refunded_at is null
		order by started_at`, id)
	if err != nil {
		return err
	}
	var plays []models.CompetitionAttempt
	for rows.Next() {
		var a models.CompetitionAttempt
		if err := rows.Scan(attemptDest(&a)...); err != nil {
			rows.Close()
			return err
		}
		plays = append(plays, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range plays {
		if err := refundPlay(ctx, q, a, reason, actor); err != nil {
			return err
		}
	}
	if round.Current > 0 {
		withdrawn := reason + ": prize withdrawn"
		if _, err := postPrize(ctx, q, round, prizeMove{
			EntryType: models.PrizeEntryCorrection, Delta: -round.Current, Reason: &withdrawn, Actor: actor,
		}); err != nil {
			return err
		}
	}
	return nil
}

// SettleWinners records the payout of every validated winner that has not
// been paid: one WINNER_SETTLEMENT entry each, taking their share off the
// prize (spec §9). Retrying settles only what is still pending.
func (r *CompetitionRepository) SettleWinners(ctx context.Context, id uuid.UUID, reference *string, actor Actor, audit models.AuditEntry) ([]uuid.UUID, error) {
	var settled []uuid.UUID
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		round, err := lockRound(ctx, tx, id)
		if err != nil {
			return err
		}
		if round.Status != "finalised" {
			return ErrCompetitionStateChanged
		}
		rows, err := tx.Query(ctx, `
			select id, prize_position, coalesce(prize_value_cents, 0)
			from competition.competition_winners
			where competition_id = $1 and status = 'validated' and settlement_status = 'pending'
			order by prize_position
			for update`, id)
		if err != nil {
			return err
		}
		type due struct {
			ID       uuid.UUID
			Position int
			Value    int64
		}
		var pending []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.ID, &d.Position, &d.Value); err != nil {
				rows.Close()
				return err
			}
			pending = append(pending, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range pending {
			status := "settled"
			if d.Value > 0 {
				reason := fmt.Sprintf("prize paid to position %d", d.Position)
				winnerID := d.ID
				if _, err := postPrize(ctx, tx, round, prizeMove{
					EntryType: models.PrizeEntryWinnerSettlement, Delta: -d.Value, WinnerID: &winnerID,
					Reason: &reason, Actor: actor,
				}); err != nil {
					return err
				}
			} else {
				status = "not_applicable"
			}
			if _, err := tx.Exec(ctx, `
				update competition.competition_winners
				set settlement_status = $2, settlement_reference = $3, settled_at = now(), updated_at = now()
				where id = $1`, d.ID, status, reference); err != nil {
				return err
			}
			settled = append(settled, d.ID)
		}
		audit.After = map[string]any{"settled_winner_ids": settled, "reference": reference}
		return insertAudit(ctx, tx, audit)
	})
	return settled, err
}

// ─── Chance rounds: finalising and drawing ─────────────────────────────────

// FinaliseInstant declares an instant-win round's result once it has closed.
// Its winner (if any play won) was recorded at the moment of the win.
func (r *CompetitionRepository) FinaliseInstant(ctx context.Context, id uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		round, err := lockRound(ctx, tx, id)
		if err != nil {
			return err
		}
		if (round.Status != "closed" && round.Status != "frozen") || round.WinnerMethod != "instant" {
			return ErrCompetitionStateChanged
		}
		if _, err := tx.Exec(ctx, `
			update competition.competitions
			set status = 'finalised', finalised_at = now(),
			    final_prize_cents = coalesce(final_prize_cents, current_prize_cents), updated_at = now()
			where id = $1`, id); err != nil {
			return err
		}
		if err := expireSessions(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// DrawEntries lists a prize draw's entries — every play that has not been
// voided — in a fixed order, so the same list can be rebuilt to check a draw.
func (r *CompetitionRepository) DrawEntries(ctx context.Context, id uuid.UUID) ([]games.DrawEntry, error) {
	return drawEntries(ctx, r.db, id)
}

func drawEntries(ctx context.Context, q querier, id uuid.UUID) ([]games.DrawEntry, error) {
	rows, err := q.Query(ctx, `
		select id::text, customer_id::text from competition.competition_attempts
		where competition_id = $1 and status <> 'voided'
		order by started_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]games.DrawEntry, 0)
	for rows.Next() {
		var e games.DrawEntry
		if err := rows.Scan(&e.PlayID, &e.CustomerID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DrawRecord is everything a prize draw used and chose.
type DrawRecord struct {
	Entries      []games.DrawEntry
	EntriesHash  string
	RandomValues []int64
	Picks        []games.DrawPick
	AdminID      *uuid.UUID
}

// PrizeDraw is a stored draw, for the admin audit view.
type PrizeDraw struct {
	ID            uuid.UUID       `json:"id"`
	EntryCount    int             `json:"entry_count"`
	EntriesSHA256 string          `json:"entries_sha256"`
	RandomValues  json.RawMessage `json:"random_values"`
	Picks         json.RawMessage `json:"picks"`
	Algorithm     string          `json:"algorithm"`
	CreatedAt     time.Time       `json:"created_at"`
}

// ApplyDraw stores a prize draw, writes its winners and finalises the round,
// all at once. It re-reads the entries under the round lock and refuses if
// they changed since the draw was made, so a void landing mid-draw cannot
// leave the record describing a different list.
func (r *CompetitionRepository) ApplyDraw(ctx context.Context, id uuid.UUID, rec DrawRecord, plans []WinnerPlan, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		round, err := lockRound(ctx, tx, id)
		if err != nil {
			return err
		}
		if (round.Status != "closed" && round.Status != "frozen") || round.WinnerMethod != "draw" {
			return ErrCompetitionStateChanged
		}
		current, err := drawEntries(ctx, tx, id)
		if err != nil {
			return err
		}
		if games.DrawEntriesHash(current) != rec.EntriesHash {
			return ErrCompetitionStateChanged
		}
		entries, _ := json.Marshal(rec.Entries)
		randoms, _ := json.Marshal(rec.RandomValues)
		picks, _ := json.Marshal(rec.Picks)
		_, err = tx.Exec(ctx, `
			insert into competition.prize_draws
				(competition_id, entry_count, entries_sha256, entries, random_values, picks, algorithm, drawn_by_admin_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, len(rec.Entries), rec.EntriesHash, entries, randoms, picks, games.ChanceAlgorithm, rec.AdminID)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrCompetitionStateChanged
		}
		if err != nil {
			return err
		}
		for _, p := range plans {
			if err := insertWinner(ctx, tx, id, p); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			update competition.competitions
			set status = 'finalised', finalised_at = now(),
			    final_prize_cents = coalesce(final_prize_cents, current_prize_cents),
			    prize_version = prize_version + 1, updated_at = now()
			where id = $1`, id); err != nil {
			return err
		}
		if err := expireSessions(ctx, tx, id); err != nil {
			return err
		}
		if err := insertOutbox(ctx, tx, id, "STATUS_CHANGED", map[string]any{
			"event": "STATUS_CHANGED", "game_id": id, "status": "finalised", "reason": "draw",
			"current_prize_cents": round.Current, "eligible_play_count": round.Plays, "version": round.Version + 1,
		}); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// GetDraw returns a round's stored prize draw, or nil when none was run.
func (r *CompetitionRepository) GetDraw(ctx context.Context, id uuid.UUID) (*PrizeDraw, error) {
	var d PrizeDraw
	err := r.db.QueryRow(ctx, `
		select id, entry_count, entries_sha256, random_values, picks, algorithm, created_at
		from competition.prize_draws where competition_id = $1`, id).
		Scan(&d.ID, &d.EntryCount, &d.EntriesSHA256, &d.RandomValues, &d.Picks, &d.Algorithm, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ─── Reads for the admin ledger and dashboard ─────────────────────────────

// Ledger lists a round's prize ledger, oldest first.
func (r *CompetitionRepository) Ledger(ctx context.Context, id uuid.UUID) ([]models.PrizeLedgerEntry, error) {
	rows, err := r.db.Query(ctx, `
		select id, seq, competition_id, attempt_id, winner_id, entry_type, amount_delta_cents,
		       balance_after_cents, reason, actor_type, actor_id, created_at
		from competition.prize_ledger
		where competition_id = $1
		order by seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.PrizeLedgerEntry, 0)
	for rows.Next() {
		var e models.PrizeLedgerEntry
		if err := rows.Scan(&e.ID, &e.Seq, &e.CompetitionID, &e.AttemptID, &e.WinnerID, &e.EntryType,
			&e.AmountDeltaCents, &e.BalanceAfterCents, &e.Reason, &e.ActorType, &e.ActorID, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Plays lists a round's plays, newest first.
func (r *CompetitionRepository) Plays(ctx context.Context, id uuid.UUID, limit int) ([]models.AdminPlay, error) {
	rows, err := r.db.Query(ctx, `
		select a.id, a.customer_id, c.display_name, a.attempt_number, a.status, a.points_spent,
		       a.prize_increment_cents, a.prize_after_cents, s.score, a.void_reason, a.refunded_at, a.started_at,
		       a.result_payload
		from competition.competition_attempts a
		inner join customer.customers c on c.id = a.customer_id
		left join competition.score_submissions s on s.attempt_id = a.id
		where a.competition_id = $1
		order by a.started_at desc
		limit $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.AdminPlay, 0)
	for rows.Next() {
		var p models.AdminPlay
		if err := rows.Scan(&p.ID, &p.CustomerID, &p.DisplayName, &p.AttemptNumber, &p.Status, &p.PointsSpent,
			&p.PrizeIncrementCents, &p.PrizeAfterCents, &p.Score, &p.VoidReason, &p.RefundedAt, &p.StartedAt,
			&p.Result); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Reconcile recomputes a round's prize from its ledger and checks every link
// between plays, points and prize entries (spec §9, AC-12). The result is
// stored; a discrepancy is also written to the audit log. Nothing is ever
// corrected automatically.
func (r *CompetitionRepository) Reconcile(ctx context.Context, id uuid.UUID) (*models.Reconciliation, error) {
	var v struct {
		cached, plays                                     int64
		ledgerSum, lastBalance, entries                   int64
		activePlays, incrementPlays, incrementEntries     int64
		mismatchedIncrements, orphanReversals             int64
		charged, debits, debitMismatch, refunded, refunds int64
		refundMismatch                                    int64
		max                                               *int64
	}
	err := r.db.QueryRow(ctx, `
		select c.current_prize_cents, c.eligible_play_count, c.max_prize_cents,
		       coalesce((select sum(amount_delta_cents) from competition.prize_ledger where competition_id = c.id), 0),
		       coalesce((select balance_after_cents from competition.prize_ledger
		                  where competition_id = c.id order by seq desc limit 1), 0),
		       (select count(*) from competition.prize_ledger where competition_id = c.id),
		       (select count(*) from competition.competition_attempts where competition_id = c.id and status <> 'voided'),
		       (select count(*) from competition.competition_attempts where competition_id = c.id and prize_increment_cents > 0),
		       (select count(*) from competition.prize_ledger where competition_id = c.id and entry_type = 'play_increment'),
		       (select count(*) from competition.prize_ledger l
		          join competition.competition_attempts a on a.id = l.attempt_id
		         where l.competition_id = c.id and l.entry_type = 'play_increment'
		           and l.amount_delta_cents <> a.prize_increment_cents),
		       (select count(*) from competition.prize_ledger l
		          join competition.competition_attempts a on a.id = l.attempt_id
		         where l.competition_id = c.id and l.entry_type = 'play_reversal'
		           and (a.status <> 'voided' or l.amount_delta_cents <> -a.prize_increment_cents)),
		       (select count(*) from competition.competition_attempts where competition_id = c.id and points_spent > 0),
		       (select count(*) from finance.points_ledger where competition_id = c.id and entry_type = 'play_debit'),
		       (select count(*) from competition.competition_attempts a
		         where a.competition_id = c.id and a.points_spent > 0 and not exists (
		           select 1 from finance.points_ledger p
		            where p.id = a.points_ledger_id and p.entry_type = 'play_debit'
		              and p.amount_delta = -a.points_spent and p.attempt_id = a.id)),
		       (select count(*) from competition.competition_attempts
		         where competition_id = c.id and refunded_at is not null),
		       (select count(*) from finance.points_ledger where competition_id = c.id and entry_type = 'play_refund'),
		       (select count(*) from competition.competition_attempts a
		         where a.competition_id = c.id and a.refunded_at is not null and not exists (
		           select 1 from finance.points_ledger p
		            where p.attempt_id = a.id and p.entry_type = 'play_refund' and p.amount_delta = a.points_spent))
		from competition.competitions c
		where c.id = $1`, id).
		Scan(&v.cached, &v.plays, &v.max, &v.ledgerSum, &v.lastBalance, &v.entries,
			&v.activePlays, &v.incrementPlays, &v.incrementEntries, &v.mismatchedIncrements, &v.orphanReversals,
			&v.charged, &v.debits, &v.debitMismatch, &v.refunded, &v.refunds, &v.refundMismatch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCompetitionNotFound
	}
	if err != nil {
		return nil, err
	}

	// The prize the ledger says there should be (spec §1.3).
	expected := v.ledgerSum
	if expected < 0 {
		expected = 0
	}
	if v.max != nil && expected > *v.max {
		expected = *v.max
	}
	last := v.lastBalance
	if v.entries == 0 {
		last = v.ledgerSum
	}
	checks := []models.ReconciliationCheck{
		{Name: "cached prize matches ledger", Expected: expected, Actual: v.cached},
		{Name: "ledger running balance matches its sum", Expected: v.ledgerSum, Actual: last},
		{Name: "play counter matches plays", Expected: v.activePlays, Actual: v.plays},
		{Name: "every increment has one ledger entry", Expected: v.incrementPlays, Actual: v.incrementEntries},
		{Name: "increment entries match their plays", Expected: 0, Actual: v.mismatchedIncrements},
		{Name: "reversals belong to voided plays", Expected: 0, Actual: v.orphanReversals},
		{Name: "every charged play has one points debit", Expected: v.charged, Actual: v.debits},
		{Name: "points debits match their plays", Expected: 0, Actual: v.debitMismatch},
		{Name: "every refunded play has one points refund", Expected: v.refunded, Actual: v.refunds},
		{Name: "points refunds match their plays", Expected: 0, Actual: v.refundMismatch},
	}
	status := "ok"
	for i := range checks {
		checks[i].OK = checks[i].Expected == checks[i].Actual
		if !checks[i].OK {
			status = "discrepancy"
		}
	}
	rec := &models.Reconciliation{
		CompetitionID: id, Status: status, LedgerTotalCents: v.ledgerSum,
		CurrentPrizeCents: v.cached, Checks: checks, CheckedAt: time.Now().UTC(),
	}

	details, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	if _, err := r.db.Exec(ctx, `
		insert into competition.reconciliation_runs (competition_id, status, details) values ($1, $2, $3)`,
		id, status, details); err != nil {
		return nil, err
	}
	if status != "ok" {
		reason := "prize reconciliation found a discrepancy"
		_ = insertAudit(ctx, r.db, models.AuditEntry{
			ActorType: "system", Action: "competition.reconciliation_discrepancy",
			EntityType: "competition", EntityID: &id, After: rec, Reason: &reason,
		})
	}
	return rec, nil
}

// ReconcilableIDs lists rounds whose money can still move, for the
// scheduled reconciliation job.
func (r *CompetitionRepository) ReconcilableIDs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `
		select id from competition.competitions
		where status in ('scheduled', 'live', 'paused', 'closed', 'frozen')
		   or (status in ('finalised', 'cancelled') and updated_at > now() - interval '7 days')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Analytics gathers the Super Admin dashboard figures for a round (spec §10).
func (r *CompetitionRepository) Analytics(ctx context.Context, id uuid.UUID) (*models.CompetitionAnalytics, error) {
	a := &models.CompetitionAnalytics{
		CompetitionID: id, RejectedByReason: map[string]int64{},
		TopPlayers: []models.PlayerActivity{}, VelocityAlerts: []models.PlayerActivity{},
	}
	var growth bool
	err := r.db.QueryRow(ctx, `
		select c.prize_currency, c.current_prize_cents, c.start_prize_cents, c.max_prize_cents,
		       c.prize_growth_enabled,
		       (select count(*) from competition.competition_attempts where competition_id = c.id and status <> 'voided'),
		       (select count(*) from competition.competition_attempts where competition_id = c.id and status = 'voided'),
		       (select count(distinct customer_id) from competition.competition_attempts
		         where competition_id = c.id and status <> 'voided'),
		       (select count(*) from (select customer_id from competition.competition_attempts
		         where competition_id = c.id and status <> 'voided'
		         group by customer_id having count(*) > 1) t),
		       coalesce((select -sum(amount_delta) from finance.points_ledger
		                  where competition_id = c.id and entry_type = 'play_debit'), 0),
		       coalesce((select sum(amount_delta) from finance.points_ledger
		                  where competition_id = c.id and entry_type = 'play_refund'), 0),
		       coalesce((select sum(amount_delta_cents) filter (where entry_type = 'play_increment')
		                   from competition.prize_ledger where competition_id = c.id), 0),
		       coalesce((select sum(amount_delta_cents) filter (where entry_type = 'admin_adjustment')
		                   from competition.prize_ledger where competition_id = c.id), 0),
		       coalesce((select sum(amount_delta_cents) filter (where entry_type = 'play_reversal')
		                   from competition.prize_ledger where competition_id = c.id), 0),
		       coalesce((select sum(amount_delta_cents) filter (where entry_type = 'correction')
		                   from competition.prize_ledger where competition_id = c.id), 0),
		       coalesce((select -sum(amount_delta_cents) filter (where entry_type = 'winner_settlement')
		                   from competition.prize_ledger where competition_id = c.id), 0),
		       (select count(*) from competition.competition_views where competition_id = c.id)
		from competition.competitions c
		where c.id = $1`, id).
		Scan(&a.Currency, &a.CurrentPrizeCents, &a.StartPrizeCents, &a.MaxPrizeCents, &growth,
			&a.ValidPlays, &a.VoidedPlays, &a.UniquePlayers, &a.RepeatPlayers,
			&a.PointsSpent, &a.PointsRefunded, &a.IncrementsCents, &a.AdjustmentsCents,
			&a.ReversalsCents, &a.CorrectionsCents, &a.SettledCents, &a.UniqueViewers)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCompetitionNotFound
	}
	if err != nil {
		return nil, err
	}
	a.PrizeGrowthCents = a.CurrentPrizeCents + a.SettledCents - a.StartPrizeCents
	a.NetPointsConsumed = a.PointsSpent - a.PointsRefunded
	if a.UniquePlayers > 0 {
		a.RepeatPlayRate = float64(a.RepeatPlayers) / float64(a.UniquePlayers)
	}
	if a.UniqueViewers > 0 {
		a.ViewToPlayRate = float64(a.UniquePlayers) / float64(a.UniqueViewers)
	}
	switch {
	case !growth:
		start := a.StartPrizeCents
		a.MaxPossibleLiability = &start
	case a.MaxPrizeCents != nil:
		a.MaxPossibleLiability = a.MaxPrizeCents
	}

	rows, err := r.db.Query(ctx, `
		select reason_code, count(*) from competition.play_rejections
		where competition_id = $1 group by reason_code`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var code string
		var n int64
		if err := rows.Scan(&code, &n); err != nil {
			rows.Close()
			return nil, err
		}
		a.RejectedByReason[code] = n
	}
	rows.Close()

	// Concentration and velocity: the heaviest players, and anyone playing
	// unusually fast in the last hour.
	rows, err = r.db.Query(ctx, `
		select a.customer_id, c.display_name, count(*),
		       count(*) filter (where a.started_at > now() - interval '1 hour')
		from competition.competition_attempts a
		inner join customer.customers c on c.id = a.customer_id
		where a.competition_id = $1 and a.status <> 'voided'
		group by a.customer_id, c.display_name
		order by count(*) desc
		limit 50`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p models.PlayerActivity
		if err := rows.Scan(&p.CustomerID, &p.DisplayName, &p.Plays, &p.LastHour); err != nil {
			rows.Close()
			return nil, err
		}
		if len(a.TopPlayers) < 5 {
			a.TopPlayers = append(a.TopPlayers, p)
		}
		if p.LastHour >= velocityAlertPerHour {
			a.VelocityAlerts = append(a.VelocityAlerts, p)
		}
	}
	rows.Close()

	for _, src := range []struct {
		key string
		out *[]models.SharedSource
	}{{"device_id", &a.SharedDevices}, {"network", &a.SharedNetworks}} {
		*src.out = []models.SharedSource{}
		rows, err := r.db.Query(ctx, `
			select risk_metadata ->> $2, count(distinct customer_id), count(*)
			from competition.competition_attempts
			where competition_id = $1 and risk_metadata ? $2 and status <> 'voided'
			group by 1
			having count(distinct customer_id) > 1
			order by 2 desc, 3 desc
			limit 10`, id, src.key)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s models.SharedSource
			if err := rows.Scan(&s.Source, &s.Accounts, &s.Plays); err != nil {
				rows.Close()
				return nil, err
			}
			*src.out = append(*src.out, s)
		}
		rows.Close()
	}

	var checked time.Time
	var status string
	err = r.db.QueryRow(ctx, `
		select status, created_at from competition.reconciliation_runs
		where competition_id = $1 order by created_at desc limit 1`, id).Scan(&status, &checked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		a.ReconciliationStatus = "not_run"
	case err != nil:
		return nil, err
	default:
		a.ReconciliationStatus = status
		a.ReconciliationCheckedAt = &checked
	}
	return a, nil
}

// velocityAlertPerHour is how many plays in an hour flags a player for a
// look (spec §10 fraud signals).
const velocityAlertPerHour = 30

// EventsSince reads a round's committed live events after a sequence number.
func (r *CompetitionRepository) EventsSince(ctx context.Context, id uuid.UUID, after int64, limit int) ([]models.PrizeEvent, error) {
	rows, err := r.db.Query(ctx, `
		select seq, event_type, payload from core.outbox_events
		where aggregate_type = 'competition' and aggregate_id = $1 and seq > $2
		  and event_type in ('PRIZE_UPDATED', 'STATUS_CHANGED')
		order by seq
		limit $3`, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.PrizeEvent, 0)
	for rows.Next() {
		var e models.PrizeEvent
		var raw []byte
		if err := rows.Scan(&e.Seq, &e.Type, &raw); err != nil {
			return nil, err
		}
		e.Payload = raw
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneEvents drops live events old enough that no open stream can still
// need them.
func (r *CompetitionRepository) PruneEvents(ctx context.Context, olderThan time.Duration) error {
	_, err := r.db.Exec(ctx, `
		delete from core.outbox_events
		where aggregate_type = 'competition' and created_at < now() - $1::interval`,
		fmt.Sprintf("%d seconds", int(olderThan.Seconds())))
	return err
}

// LatestEventSeq is where a new live stream starts reading from.
func (r *CompetitionRepository) LatestEventSeq(ctx context.Context, id uuid.UUID) (int64, error) {
	var seq int64
	err := r.db.QueryRow(ctx, `
		select coalesce(max(seq), 0) from core.outbox_events
		where aggregate_type = 'competition' and aggregate_id = $1`, id).Scan(&seq)
	return seq, err
}
