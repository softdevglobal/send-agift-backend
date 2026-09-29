package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrPointsInsufficient = errors.New("not enough points")
	ErrPointsDuplicate    = errors.New("points change already recorded")
)

// PointsRepository keeps customers' points: a balance per customer that can
// never go negative, and an append-only ledger explaining every change.
type PointsRepository struct {
	db *pgxpool.Pool
}

func NewPointsRepository(db *pgxpool.Pool) *PointsRepository {
	return &PointsRepository{db: db}
}

// PointsChange is one movement of a customer's points.
type PointsChange struct {
	CustomerID     uuid.UUID
	EntryType      string
	Delta          int64
	CompetitionID  *uuid.UUID
	AttemptID      *uuid.UUID
	OrderID        *uuid.UUID
	IdempotencyKey string
	Reason         *string
	ActorType      string
	ActorID        *uuid.UUID
	// EntryID lets the caller fix the ledger row's id in advance, for rows
	// that another row in the same transaction points at.
	EntryID *uuid.UUID
}

// lockPointsAccount returns the customer's balance with the account row
// locked for the rest of the transaction, opening an empty account on first
// use.
func lockPointsAccount(ctx context.Context, q querier, customerID uuid.UUID) (int64, error) {
	if _, err := q.Exec(ctx, `
		insert into finance.points_accounts (customer_id) values ($1)
		on conflict (customer_id) do nothing`, customerID); err != nil {
		return 0, err
	}
	var balance int64
	err := q.QueryRow(ctx, `
		select balance from finance.points_accounts where customer_id = $1 for update`, customerID).
		Scan(&balance)
	return balance, err
}

// applyPoints moves a customer's balance and writes its ledger row. The
// caller holds the account lock. A negative result is refused before the
// database's own check would fire.
func applyPoints(ctx context.Context, q querier, ch PointsChange) (entryID uuid.UUID, balance int64, err error) {
	balance, err = lockPointsAccount(ctx, q, ch.CustomerID)
	if err != nil {
		return uuid.Nil, 0, err
	}
	if balance+ch.Delta < 0 {
		return uuid.Nil, balance, ErrPointsInsufficient
	}
	earned, spent := int64(0), int64(0)
	switch {
	case ch.EntryType == models.PointsEntryPlayDebit:
		spent = -ch.Delta
	case ch.EntryType == models.PointsEntryPlayRefund:
		spent = -ch.Delta // a refund gives back what was spent
	case ch.EntryType == models.PointsEntryOrderReversal:
		earned = ch.Delta // takes back what the order earned
	case ch.Delta > 0:
		earned = ch.Delta
	}
	if err = q.QueryRow(ctx, `
		update finance.points_accounts
		set balance = balance + $2,
		    lifetime_earned = greatest(lifetime_earned + $3, 0),
		    lifetime_spent = greatest(lifetime_spent + $4, 0),
		    updated_at = now()
		where customer_id = $1
		returning balance`, ch.CustomerID, ch.Delta, earned, spent).Scan(&balance); err != nil {
		return uuid.Nil, 0, err
	}
	id := uuid.New()
	if ch.EntryID != nil {
		id = *ch.EntryID
	}
	_, err = q.Exec(ctx, `
		insert into finance.points_ledger
			(id, customer_id, entry_type, amount_delta, balance_after, competition_id, attempt_id,
			 idempotency_key, reason, actor_type, actor_id, order_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		id, ch.CustomerID, ch.EntryType, ch.Delta, balance, ch.CompetitionID, ch.AttemptID,
		ch.IdempotencyKey, ch.Reason, ch.ActorType, ch.ActorID, ch.OrderID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return uuid.Nil, 0, ErrPointsDuplicate
	}
	return id, balance, err
}

// Balance is a customer's current points, zero when they have no account.
func (r *PointsRepository) Balance(ctx context.Context, customerID uuid.UUID) (int64, error) {
	var balance int64
	err := r.db.QueryRow(ctx, `
		select coalesce((select balance from finance.points_accounts where customer_id = $1), 0)`,
		customerID).Scan(&balance)
	return balance, err
}

// Wallet is a customer's balance, lifetime totals and latest entries.
func (r *PointsRepository) Wallet(ctx context.Context, customerID uuid.UUID, limit int) (*models.PointsWallet, error) {
	w := &models.PointsWallet{CustomerID: customerID, Entries: []models.PointsEntry{}}
	err := r.db.QueryRow(ctx, `
		select balance, lifetime_earned, lifetime_spent
		from finance.points_accounts where customer_id = $1`, customerID).
		Scan(&w.Balance, &w.LifetimeEarned, &w.LifetimeSpent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		select l.id, l.entry_type, l.amount_delta, l.balance_after, l.competition_id, l.attempt_id,
		       l.order_id, l.reason, l.actor_type, l.created_at, c.title
		from finance.points_ledger l
		left join competition.competitions c on c.id = l.competition_id
		where l.customer_id = $1
		order by l.seq desc
		limit $2`, customerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e models.PointsEntry
		if err := rows.Scan(&e.ID, &e.EntryType, &e.AmountDelta, &e.BalanceAfter, &e.CompetitionID,
			&e.AttemptID, &e.OrderID, &e.Reason, &e.ActorType, &e.CreatedAt, &e.CompetitionTitle); err != nil {
			return nil, err
		}
		w.Entries = append(w.Entries, e)
	}
	return w, rows.Err()
}

// applyOne runs one points change in its own transaction. A change already
// recorded under its key is reported as not applied, without error.
func (r *PointsRepository) applyOne(ctx context.Context, ch PointsChange) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, _, err := applyPoints(ctx, tx, ch); err != nil {
		if errors.Is(err, ErrPointsDuplicate) {
			return false, nil
		}
		return false, err
	}
	return true, tx.Commit(ctx)
}

// Adjust applies an admin grant or deduction with its audit row. A retried
// request with the same idempotency key is refused as a duplicate rather
// than applied twice.
func (r *PointsRepository) Adjust(ctx context.Context, ch PointsChange, audit models.AuditEntry) (int64, error) {
	var balance int64
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, balance, err = applyPoints(ctx, tx, ch); err != nil {
		return balance, err
	}
	audit.After = map[string]any{
		"customer_id": ch.CustomerID, "entry_type": ch.EntryType, "amount_delta": ch.Delta,
		"balance_after": balance,
	}
	if err := insertAudit(ctx, tx, audit); err != nil {
		return 0, err
	}
	return balance, tx.Commit(ctx)
}

// SearchCustomers finds customers by email or name for the admin points
// tools, newest first when there is no query.
func (r *PointsRepository) SearchCustomers(ctx context.Context, query string, limit int) ([]models.CustomerPointsSummary, error) {
	rows, err := r.db.Query(ctx, `
		select c.id, c.email::text, c.display_name, co.name, c.status,
		       coalesce(a.balance, 0), c.created_at
		from customer.customers c
		inner join core.countries co on co.id = c.country_id
		left join finance.points_accounts a on a.customer_id = c.id
		where c.deleted_at is null
		  and ($1 = '' or c.email::text ilike '%' || $1 || '%' or c.display_name ilike '%' || $1 || '%')
		order by c.created_at desc
		limit $2`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.CustomerPointsSummary, 0)
	for rows.Next() {
		var c models.CustomerPointsSummary
		if err := rows.Scan(&c.CustomerID, &c.Email, &c.DisplayName, &c.CountryName, &c.Status,
			&c.Balance, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ─── Earning ──────────────────────────────────────────────────────────────

// currencyExponent is how many minor-unit digits a currency has. Most have
// two; these have none.
var zeroDecimalCurrencies = map[string]bool{
	"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true, "UGX": true,
	"PYG": true, "RWF": true, "XAF": true, "XOF": true, "KMF": true, "GNF": true,
}

// PointsForAmount is what an order total earns: points_per_unit for every
// whole unit of the currency, rounded down.
func PointsForAmount(minor int64, currency string, pointsPerUnit int) int64 {
	if minor <= 0 || pointsPerUnit <= 0 {
		return 0
	}
	if zeroDecimalCurrencies[currency] {
		return minor * int64(pointsPerUnit)
	}
	return minor * int64(pointsPerUnit) / 100
}

// EarningRules lists every country with its rule (or the empty default).
func (r *PointsRepository) EarningRules(ctx context.Context) ([]models.PointsEarningRule, error) {
	rows, err := r.db.Query(ctx, `
		select co.id, co.name, co.default_currency,
		       coalesce(pr.enabled, false), coalesce(pr.points_per_unit, 0), pr.effective_from,
		       coalesce(pr.signup_bonus, 0), pr.signup_bonus_since,
		       coalesce(cc.points_earning_enabled, false), pr.updated_at
		from core.countries co
		left join finance.points_earning_rules pr on pr.country_id = co.id
		left join core.country_capabilities cc on cc.country_id = co.id
		order by co.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.PointsEarningRule, 0)
	for rows.Next() {
		var rule models.PointsEarningRule
		if err := rows.Scan(&rule.CountryID, &rule.CountryName, &rule.Currency, &rule.Enabled,
			&rule.PointsPerUnit, &rule.EffectiveFrom, &rule.SignupBonus, &rule.SignupBonusSince,
			&rule.EarningAllowed, &rule.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// RuleForCustomer is the earning rule of the customer's country.
func (r *PointsRepository) RuleForCustomer(ctx context.Context, customerID uuid.UUID) (*models.PointsEarningRule, error) {
	var rule models.PointsEarningRule
	err := r.db.QueryRow(ctx, `
		select co.id, co.name, co.default_currency,
		       coalesce(pr.enabled, false), coalesce(pr.points_per_unit, 0), pr.effective_from,
		       coalesce(pr.signup_bonus, 0), pr.signup_bonus_since,
		       coalesce(cc.points_earning_enabled, false), pr.updated_at
		from customer.customers c
		inner join core.countries co on co.id = c.country_id
		left join finance.points_earning_rules pr on pr.country_id = co.id
		left join core.country_capabilities cc on cc.country_id = co.id
		where c.id = $1`, customerID).
		Scan(&rule.CountryID, &rule.CountryName, &rule.Currency, &rule.Enabled,
			&rule.PointsPerUnit, &rule.EffectiveFrom, &rule.SignupBonus, &rule.SignupBonusSince,
			&rule.EarningAllowed, &rule.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCustomerNotFound
	}
	return &rule, err
}

// SetEarningRule saves a country's rule with its audit row. Switching earning
// on starts the clock for which deliveries count; switching a sign-up bonus
// on starts it for which sign-ups count — neither pays out the past.
func (r *PointsRepository) SetEarningRule(ctx context.Context, countryID uuid.UUID, enabled bool, perUnit, bonus int, adminID uuid.UUID, audit models.AuditEntry) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
		insert into finance.points_earning_rules
			(country_id, enabled, points_per_unit, signup_bonus, updated_by_admin_id)
		values ($1, $2, $3, $4, $5)
		on conflict (country_id) do update set
			effective_from = case when not finance.points_earning_rules.enabled and excluded.enabled
			                      then now() else finance.points_earning_rules.effective_from end,
			signup_bonus_since = case when finance.points_earning_rules.signup_bonus = 0 and excluded.signup_bonus > 0
			                          then now() else finance.points_earning_rules.signup_bonus_since end,
			enabled = excluded.enabled,
			points_per_unit = excluded.points_per_unit,
			signup_bonus = excluded.signup_bonus,
			updated_by_admin_id = excluded.updated_by_admin_id,
			updated_at = now()`, countryID, enabled, perUnit, bonus, adminID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrCountryNotFound
		}
		return err
	}
	if err := insertAudit(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RunEarning is one pass of the earning job: points for newly delivered
// orders, points taken back from refunded or cancelled ones, and sign-up
// bonuses. Every award is keyed by what earned it, so a pass can be repeated
// or overlap another without ever paying twice.
func (r *PointsRepository) RunEarning(ctx context.Context, batch int) (*models.EarningRunResult, error) {
	res := &models.EarningRunResult{}

	type orderAward struct {
		orderID, customerID uuid.UUID
		total               int64
		currency, number    string
		perUnit             int
	}
	rows, err := r.db.Query(ctx, `
		select o.id, o.customer_id, o.total_amount, o.currency, o.order_number, pr.points_per_unit
		from marketplace.orders o
		inner join finance.points_earning_rules pr
		        on pr.country_id = o.country_id and pr.enabled and pr.points_per_unit > 0
		inner join core.country_capabilities cc
		        on cc.country_id = o.country_id and cc.points_earning_enabled
		where o.status = 'delivered' and o.updated_at >= pr.effective_from
		  and not exists (select 1 from finance.points_ledger l
		                  where l.idempotency_key = 'order:' || o.id::text)
		order by o.updated_at
		limit $1`, batch)
	if err != nil {
		return nil, err
	}
	var awards []orderAward
	for rows.Next() {
		var a orderAward
		if err := rows.Scan(&a.orderID, &a.customerID, &a.total, &a.currency, &a.number, &a.perUnit); err != nil {
			rows.Close()
			return nil, err
		}
		awards = append(awards, a)
	}
	rows.Close()
	for _, a := range awards {
		points := PointsForAmount(a.total, a.currency, a.perUnit)
		if points <= 0 {
			continue
		}
		reason := "order " + a.number + " delivered"
		orderID := a.orderID
		ok, err := r.applyOne(ctx, PointsChange{
			CustomerID: a.customerID, EntryType: models.PointsEntryOrderReward, Delta: points,
			OrderID: &orderID, IdempotencyKey: "order:" + a.orderID.String(),
			Reason: &reason, ActorType: "system",
		})
		if err != nil {
			return res, err
		}
		if ok {
			res.OrdersRewarded++
			res.PointsAwarded += points
		}
	}

	// Refunded or cancelled after earning: take the points back, up to what
	// the customer still holds.
	type reversal struct {
		orderID, customerID uuid.UUID
		earned              int64
		number              string
	}
	rows, err = r.db.Query(ctx, `
		select l.order_id, l.customer_id, l.amount_delta, o.order_number
		from finance.points_ledger l
		inner join marketplace.orders o on o.id = l.order_id
		where l.entry_type = 'order_reward' and o.status in ('refunded', 'cancelled')
		  and not exists (select 1 from finance.points_ledger x
		                  where x.idempotency_key = 'order-reversal:' || l.order_id::text)
		limit $1`, batch)
	if err != nil {
		return res, err
	}
	var reversals []reversal
	for rows.Next() {
		var v reversal
		if err := rows.Scan(&v.orderID, &v.customerID, &v.earned, &v.number); err != nil {
			rows.Close()
			return res, err
		}
		reversals = append(reversals, v)
	}
	rows.Close()
	for _, v := range reversals {
		balance, err := r.Balance(ctx, v.customerID)
		if err != nil {
			return res, err
		}
		take := v.earned
		if take > balance {
			take = balance
		}
		if take <= 0 {
			continue // nothing to take yet; tried again next pass
		}
		reason := "order " + v.number + " refunded"
		if take < v.earned {
			reason += " (only the points still held were taken back)"
		}
		orderID := v.orderID
		ok, err := r.applyOne(ctx, PointsChange{
			CustomerID: v.customerID, EntryType: models.PointsEntryOrderReversal, Delta: -take,
			OrderID: &orderID, IdempotencyKey: "order-reversal:" + v.orderID.String(),
			Reason: &reason, ActorType: "system",
		})
		if errors.Is(err, ErrPointsInsufficient) {
			continue // spent in the meantime; tried again next pass
		}
		if err != nil {
			return res, err
		}
		if ok {
			res.OrdersReversed++
			res.PointsReversed += take
		}
	}

	type signup struct {
		customerID uuid.UUID
		bonus      int64
	}
	rows, err = r.db.Query(ctx, `
		select c.id, pr.signup_bonus
		from customer.customers c
		inner join finance.points_earning_rules pr
		        on pr.country_id = c.country_id and pr.enabled and pr.signup_bonus > 0
		inner join core.country_capabilities cc
		        on cc.country_id = c.country_id and cc.points_earning_enabled
		where c.created_at >= pr.signup_bonus_since and c.deleted_at is null and c.status = 'active'
		  and not exists (select 1 from finance.points_ledger l
		                  where l.idempotency_key = 'signup:' || c.id::text)
		limit $1`, batch)
	if err != nil {
		return res, err
	}
	var signups []signup
	for rows.Next() {
		var s signup
		if err := rows.Scan(&s.customerID, &s.bonus); err != nil {
			rows.Close()
			return res, err
		}
		signups = append(signups, s)
	}
	rows.Close()
	reason := "welcome bonus"
	for _, s := range signups {
		ok, err := r.applyOne(ctx, PointsChange{
			CustomerID: s.customerID, EntryType: models.PointsEntrySignupBonus, Delta: s.bonus,
			IdempotencyKey: "signup:" + s.customerID.String(), Reason: &reason, ActorType: "system",
		})
		if err != nil {
			return res, err
		}
		if ok {
			res.SignupBonuses++
			res.BonusPointsPaid += s.bonus
		}
	}
	return res, nil
}
