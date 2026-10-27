package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
)

var (
	ErrEntryNotFound    = errors.New("entry not found")
	ErrEntryNotHeld     = errors.New("entry is no longer reserved")
	ErrEntryStale       = errors.New("the competition's rules or cost changed since the quote")
	ErrEntryRequired    = errors.New("this competition is entered with competition points: reserve an entry first")
	ErrNotCompetitionCP = errors.New("this competition does not use competition points")
)

// EntryLimitError is a customer at the round's attempt limit, counting
// entries still held.
type EntryLimitError struct{ Limit int }

func (e *EntryLimitError) Error() string {
	return fmt.Sprintf("entry limit of %d reached for this competition", e.Limit)
}

// RoundForEntry is what a quote and a reservation are checked against.
type RoundForEntry struct {
	ID             uuid.UUID
	Title          string
	Status         string
	StartsAt       time.Time
	EndsAt         time.Time
	PointsUnit     string
	Cost           int64
	RulesVersion   int
	AllowedSources []string
	MaxAttempts    int
	CurrentPrize   int64
	MaxPrize       *int64
	Currency       string
	WinnerMethod   string
	GameType       string
	CountryIDs     []uuid.UUID
	Now            time.Time
}

func roundForEntry(ctx context.Context, q querier, id uuid.UUID, lock bool) (*RoundForEntry, error) {
	sql := `
		select c.id, c.title, c.status, c.starts_at, c.ends_at, c.points_unit, c.points_per_attempt,
		       c.config_version, c.cp_allowed_sources, c.max_attempts_per_customer, c.current_prize_cents,
		       c.max_prize_cents, coalesce(c.prize_currency, ''), c.winner_method, g.game_type,
		       coalesce((select array_agg(cc.country_id) from competition.competition_countries cc
		                 where cc.competition_id = c.id), '{}'), now()
		from competition.competitions c
		inner join competition.game_versions v on v.id = c.game_version_id
		inner join competition.games g on g.id = v.game_id
		where c.id = $1`
	if lock {
		sql += ` for update of c`
	}
	var r RoundForEntry
	err := q.QueryRow(ctx, sql, id).Scan(&r.ID, &r.Title, &r.Status, &r.StartsAt, &r.EndsAt, &r.PointsUnit,
		&r.Cost, &r.RulesVersion, &r.AllowedSources, &r.MaxAttempts, &r.CurrentPrize, &r.MaxPrize, &r.Currency,
		&r.WinnerMethod, &r.GameType, &r.CountryIDs, &r.Now)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCompetitionNotFound
	}
	return &r, err
}

// RoundForEntry reads a round for a quote, without locking it.
func (r *CompetitionPointsRepository) RoundForEntry(ctx context.Context, id uuid.UUID) (*RoundForEntry, error) {
	return roundForEntry(ctx, r.db, id, false)
}

// Open is whether entries are accepted right now by the database clock.
func (r *RoundForEntry) Open() bool {
	return (r.Status == "live" || r.Status == "scheduled") && !r.Now.Before(r.StartsAt) && r.Now.Before(r.EndsAt)
}

// Balances is a customer's competition points: everything available, and
// what the given sources may spend right now.
func (r *CompetitionPointsRepository) Balances(ctx context.Context, customerID uuid.UUID, sources []string) (total, eligible int64, err error) {
	err = r.db.QueryRow(ctx, `
		select coalesce(sum(available) filter (where expires_at is null or expires_at > now() or grace_until > now()), 0),
		       coalesce(sum(available) filter (where source = any($2)
		                                       and (expires_at is null or expires_at > now() or grace_until > now())), 0)
		from points.cp_lots where customer_id = $1`, customerID, sources).Scan(&total, &eligible)
	return total, eligible, err
}

// ReserveInput is one confirmed entry request.
type ReserveInput struct {
	CompetitionID   uuid.UUID
	CustomerID      uuid.UUID
	Channel         string
	RulesVersion    int
	Cost            int64
	ClientRequestID string
	RequestHash     string
	// Sources are the lot sources both the round and the customer's
	// approvals allow.
	Sources []string
	HoldTTL time.Duration
}

const entryColumns = `e.id, e.competition_id, c.title, e.rules_version, e.cost, e.channel, e.state,
	e.attempt_id, e.hold_expires_at, e.reserved_at, e.captured_at, e.released_at, e.release_reason`

func scanEntry(row scanner) (*models.CPEntry, error) {
	var e models.CPEntry
	err := row.Scan(&e.ID, &e.CompetitionID, &e.CompetitionTag, &e.RulesVersion, &e.Cost, &e.Channel, &e.State,
		&e.AttemptID, &e.HoldExpiresAt, &e.ReservedAt, &e.CapturedAt, &e.ReleasedAt, &e.ReleaseReason)
	return &e, err
}

func getEntry(ctx context.Context, q querier, id uuid.UUID) (*models.CPEntry, error) {
	e, err := scanEntry(q.QueryRow(ctx, `select `+entryColumns+`
		from competition.cp_entries e inner join competition.competitions c on c.id = e.competition_id
		where e.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	return e, err
}

// GetEntry reads one entry, which must be the customer's.
func (r *CompetitionPointsRepository) GetEntry(ctx context.Context, customerID, id uuid.UUID) (*models.CPEntry, error) {
	e, err := scanEntry(r.db.QueryRow(ctx, `select `+entryColumns+`
		from competition.cp_entries e inner join competition.competitions c on c.id = e.competition_id
		where e.id = $1 and e.customer_id = $2`, id, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	return e, err
}

// PriorEntry is the entry an earlier request with this key made, if any.
// The same key with a different request is ErrKeyReused.
func (r *CompetitionPointsRepository) PriorEntry(ctx context.Context, customerID uuid.UUID, key, hash string) (*models.CPEntry, error) {
	var (
		id    uuid.UUID
		prior string
	)
	err := r.db.QueryRow(ctx, `select id, request_hash from competition.cp_entries
		where customer_id = $1 and client_request_id = $2`, customerID, key).Scan(&id, &prior)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if prior != hash {
		return nil, ErrKeyReused
	}
	e, err := getEntry(ctx, r.db, id)
	if err == nil {
		e.Replayed = true
	}
	return e, err
}

// ReserveEntry atomically re-checks the round, holds the eligible points
// and creates one entry. A repeated request with the same key returns the
// original entry; the same key with a different request is refused.
func (r *CompetitionPointsRepository) ReserveEntry(ctx context.Context, in ReserveInput) (*models.CPEntry, error) {
	var out *models.CPEntry
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var (
			priorID   uuid.UUID
			priorHash string
		)
		err := tx.QueryRow(ctx, `select id, request_hash from competition.cp_entries
			where customer_id = $1 and client_request_id = $2`, in.CustomerID, in.ClientRequestID).
			Scan(&priorID, &priorHash)
		switch {
		case err == nil:
			if priorHash != in.RequestHash {
				return ErrKeyReused
			}
			out, err = getEntry(ctx, tx, priorID)
			if err == nil {
				out.Replayed = true
			}
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}

		round, err := roundForEntry(ctx, tx, in.CompetitionID, true)
		if err != nil {
			return err
		}
		if round.PointsUnit != "competition" {
			return ErrNotCompetitionCP
		}
		if !round.Open() {
			return ErrRoundNotLive
		}
		if round.RulesVersion != in.RulesVersion || round.Cost != in.Cost {
			return ErrEntryStale
		}
		// Caps count plays already made and entries still held.
		if round.MaxAttempts > 0 {
			var used int
			if err := tx.QueryRow(ctx, `
				select (select count(*) from competition.competition_attempts
				        where competition_id = $1 and customer_id = $2 and status <> 'voided')
				     + (select count(*) from competition.cp_entries
				        where competition_id = $1 and customer_id = $2 and state in ('reserved', 'uncertain'))`,
				in.CompetitionID, in.CustomerID).Scan(&used); err != nil {
				return err
			}
			if used >= round.MaxAttempts {
				return &EntryLimitError{Limit: round.MaxAttempts}
			}
		}

		lots, err := lockSpendableLots(ctx, tx, in.CustomerID, in.Sources)
		if err != nil {
			return err
		}
		alloc, err := allocateCP(lots, in.Cost)
		if err != nil {
			return err
		}
		entryID := uuid.New()
		reason := "entry to " + round.Title
		txID, first, err := beginCPTx(ctx, tx, "cp:reserve:"+entryID.String(), "reserve", in.CustomerID,
			CPActor{Type: "customer", ID: &in.CustomerID}, &entryID, &reason)
		if err != nil {
			return err
		}
		if !first {
			return fmt.Errorf("competition points: reservation %s recorded twice", entryID)
		}
		for _, a := range alloc {
			if err := moveCP(ctx, tx, txID, a.LotID, "available", "reserved", a.Quantity); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			insert into competition.cp_entries
				(id, competition_id, customer_id, rules_version, cost, channel, client_request_id,
				 request_hash, reserve_tx_id, hold_expires_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now() + $10::interval)`,
			entryID, in.CompetitionID, in.CustomerID, in.RulesVersion, in.Cost, in.Channel,
			in.ClientRequestID, in.RequestHash, txID, fmt.Sprintf("%d seconds", int(in.HoldTTL.Seconds()))); err != nil {
			return err
		}
		for _, a := range alloc {
			if _, err := tx.Exec(ctx, `insert into competition.cp_entry_lots (entry_id, lot_id, quantity)
				values ($1, $2, $3)`, entryID, a.LotID, a.Quantity); err != nil {
				return err
			}
		}
		out, err = getEntry(ctx, tx, entryID)
		return err
	})
	return out, err
}

func entryLots(ctx context.Context, q querier, entryID uuid.UUID) ([]CPAllocation, error) {
	rows, err := q.Query(ctx, `select lot_id, quantity from competition.cp_entry_lots
		where entry_id = $1 order by lot_id`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CPAllocation
	for rows.Next() {
		var a CPAllocation
		if err := rows.Scan(&a.LotID, &a.Quantity); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type lockedEntry struct {
	ID, CompetitionID, CustomerID uuid.UUID
	State                         string
	Cost                          int64
	RulesVersion                  int
}

func lockEntry(ctx context.Context, q querier, id uuid.UUID) (*lockedEntry, error) {
	var e lockedEntry
	err := q.QueryRow(ctx, `select id, competition_id, customer_id, state, cost, rules_version
		from competition.cp_entries where id = $1 for update`, id).
		Scan(&e.ID, &e.CompetitionID, &e.CustomerID, &e.State, &e.Cost, &e.RulesVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	return &e, err
}

// captureEntry consumes a held entry for the attempt that starts it. It is
// called from StartPlay, in the same transaction as the attempt, so a start
// and its capture commit together or not at all.
func captureEntry(ctx context.Context, tx pgx.Tx, entryID, customerID, competitionID uuid.UUID, rulesVersion int, cost int64, attemptID uuid.UUID) error {
	e, err := lockEntry(ctx, tx, entryID)
	if err != nil {
		return err
	}
	if e.CustomerID != customerID || e.CompetitionID != competitionID {
		return ErrEntryNotFound
	}
	if e.State != models.EntryReserved {
		return ErrEntryNotHeld
	}
	if e.RulesVersion != rulesVersion || e.Cost != cost {
		return ErrEntryStale
	}
	alloc, err := entryLots(ctx, tx, entryID)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(alloc))
	for i, a := range alloc {
		ids[i] = a.LotID
	}
	if err := lockLots(ctx, tx, ids); err != nil {
		return err
	}
	txID, first, err := beginCPTx(ctx, tx, "cp:settle:"+entryID.String(), "capture", customerID,
		CPActor{Type: "customer", ID: &customerID}, &entryID, nil)
	if err != nil {
		return err
	}
	if !first {
		return ErrEntryNotHeld // settled already: captured or released, never both
	}
	for _, a := range alloc {
		if err := moveCP(ctx, tx, txID, a.LotID, "reserved", "consumed", a.Quantity); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `update competition.cp_entries
		set state = 'captured', attempt_id = $2, settle_tx_id = $3, captured_at = now()
		where id = $1`, entryID, attemptID, txID)
	return err
}

// releaseEntry gives a held entry's points back. Lots that expired while
// held get the restoration window, so a start that never happened does not
// cost the customer what they could have used.
func releaseEntry(ctx context.Context, tx pgx.Tx, entryID uuid.UUID, reason string, actor CPActor, window time.Duration) (bool, error) {
	e, err := lockEntry(ctx, tx, entryID)
	if err != nil {
		return false, err
	}
	if e.State != models.EntryReserved && e.State != models.EntryUncertain {
		return false, nil
	}
	alloc, err := entryLots(ctx, tx, entryID)
	if err != nil {
		return false, err
	}
	ids := make([]uuid.UUID, len(alloc))
	for i, a := range alloc {
		ids[i] = a.LotID
	}
	if err := lockLots(ctx, tx, ids); err != nil {
		return false, err
	}
	txID, first, err := beginCPTx(ctx, tx, "cp:settle:"+entryID.String(), "release", e.CustomerID, actor, &entryID, &reason)
	if err != nil {
		return false, err
	}
	if !first {
		return false, nil
	}
	for _, a := range alloc {
		if err := moveCP(ctx, tx, txID, a.LotID, "reserved", "available", a.Quantity); err != nil {
			return false, err
		}
	}
	if window > 0 {
		if _, err := tx.Exec(ctx, `update points.cp_lots
			set grace_until = greatest(coalesce(grace_until, now()), now() + $2::interval)
			where id = any($1) and expires_at is not null and expires_at <= now() + $2::interval`,
			ids, fmt.Sprintf("%d seconds", int(window.Seconds()))); err != nil {
			return false, err
		}
	}
	_, err = tx.Exec(ctx, `update competition.cp_entries
		set state = 'released', settle_tx_id = $2, released_at = now(), release_reason = $3
		where id = $1`, entryID, txID, reason)
	return err == nil, err
}

// CancelEntry releases a customer's entry that has not started.
func (r *CompetitionPointsRepository) CancelEntry(ctx context.Context, customerID, entryID uuid.UUID, window time.Duration) (*models.CPEntry, error) {
	var out *models.CPEntry
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		e, err := lockEntry(ctx, tx, entryID)
		if err != nil {
			return err
		}
		if e.CustomerID != customerID {
			return ErrEntryNotFound
		}
		if e.State == models.EntryCaptured {
			return ErrEntryNotHeld
		}
		if e.State == models.EntryUncertain {
			return ErrEntryNotHeld // only the reconciliation can settle an uncertain start
		}
		if _, err := releaseEntry(ctx, tx, entryID, "cancelled before the attempt started",
			CPActor{Type: "customer", ID: &customerID}, window); err != nil {
			return err
		}
		out, err = getEntry(ctx, tx, entryID)
		return err
	})
	return out, err
}

// ReleaseEntry releases an entry whose start failed before it began. Used
// when StartPlay refused the attempt after the reservation.
func (r *CompetitionPointsRepository) ReleaseEntry(ctx context.Context, entryID uuid.UUID, reason string, window time.Duration) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		_, err := releaseEntry(ctx, tx, entryID, reason, systemActor, window)
		return err
	})
}

// releaseStaleEntries releases holds that were never started before they
// lapsed. A start is recorded in the same transaction as its capture, so a
// lapsed reserved entry is a confirmed no-start.
func (r *CompetitionPointsRepository) releaseStaleEntries(ctx context.Context, batch int, window time.Duration, res *models.CPRunResult) error {
	ids, err := r.idList(ctx, `select id from competition.cp_entries
		where state = 'reserved' and hold_expires_at <= now() order by hold_expires_at limit $1`, batch)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var released bool
		err := r.inTx(ctx, func(tx pgx.Tx) error {
			var err error
			released, err = releaseEntry(ctx, tx, id, "the hold lapsed before the attempt started", systemActor, window)
			return err
		})
		if err != nil {
			return err
		}
		if released {
			res.EntriesReleased++
		}
	}
	var uncertain int
	if err := r.db.QueryRow(ctx, `select count(*) from competition.cp_entries where state = 'uncertain'`).
		Scan(&uncertain); err != nil {
		return err
	}
	res.EntriesUncertain = uncertain
	return nil
}

// restoreCapturedEntry gives back the points of a captured entry whose
// attempt was voided or whose round was cancelled. Each lot it used is
// restored as a new lot naming the original, with the same source, policy,
// restrictions and expiry, so restoration cannot create new entitlement.
func restoreCapturedEntry(ctx context.Context, tx querier, attemptID uuid.UUID, reason string, actor CPActor) (bool, error) {
	var (
		entryID, customerID uuid.UUID
		state               string
	)
	err := tx.QueryRow(ctx, `select id, customer_id, state from competition.cp_entries
		where attempt_id = $1 for update`, attemptID).Scan(&entryID, &customerID, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state != models.EntryCaptured {
		return true, nil
	}
	alloc, err := entryLots(ctx, tx, entryID)
	if err != nil {
		return true, err
	}
	for _, a := range alloc {
		var (
			source       string
			policyID     *uuid.UUID
			expiresAt    *time.Time
			restrictions []byte
		)
		if err := tx.QueryRow(ctx, `select source, policy_id, expires_at, restrictions from points.cp_lots
			where id = $1`, a.LotID).Scan(&source, &policyID, &expiresAt, &restrictions); err != nil {
			return true, err
		}
		lotID := a.LotID
		// A restoration has its own source id, derived from the entry and
		// the lot, so repeating it finds the same lot.
		restoreID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cp-restore:"+entryID.String()+":"+lotID.String()))
		if _, _, err := grantCPLot(ctx, tx, CPGrant{
			CustomerID: customerID, Source: source, SourceID: restoreID, PolicyID: policyID,
			Quantity: a.Quantity, ExpiresAt: expiresAt, RestoresLot: &lotID,
			Key: "cp:restore:" + entryID.String() + ":" + lotID.String(), Actor: actor, Reason: &reason,
		}); err != nil {
			return true, err
		}
		if len(restrictions) > 2 {
			if _, err := tx.Exec(ctx, `update points.cp_lots set restrictions = $2
				where source = $3 and source_id = $1`, restoreID, restrictions, source); err != nil {
				return true, err
			}
		}
	}
	return true, nil
}
