package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The competition-points ledger. Every change is one points.cp_transactions
// row, keyed by the business operation it records, with signed postings
// between a lot's buckets (and the issuance account for a new grant) that
// sum to zero. A lot's buckets are a cache of its postings written in the
// same transaction; ReconcileCompetitionPoints compares the two.
//
// The rules enforced here, and again by the database:
//   - a repeated operation key changes nothing and returns what the first
//     one recorded;
//   - a bucket never goes negative, and a lot's issued quantity is always
//     the sum of its buckets;
//   - one original grant per source (gift qualification or bonus slot).

var (
	ErrCPInsufficient = errors.New("not enough eligible competition points")
	// errCPBucketShort is a move refused because the lot no longer holds it.
	errCPBucketShort = errors.New("competition points lot does not hold that many units")
)

// cpBuckets are the columns a move may touch.
var cpBuckets = map[string]bool{
	"available": true, "reserved": true, "consumed": true, "reversed": true, "expired": true,
}

// CPActor is who did a competition-points operation.
type CPActor struct {
	Type string // system, customer or admin
	ID   *uuid.UUID
}

var systemActor = CPActor{Type: "system"}

// beginCPTx records one operation. first is false when the key was already
// used; the caller then returns what that operation did instead of doing
// it again.
func beginCPTx(ctx context.Context, q querier, key, kind string, customerID uuid.UUID, actor CPActor, correlation *uuid.UUID, reason *string) (id uuid.UUID, first bool, err error) {
	err = q.QueryRow(ctx, `
		insert into points.cp_transactions
			(operation_key, kind, customer_id, actor_type, actor_id, correlation_id, reason)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (operation_key) do nothing
		returning id`, key, kind, customerID, actor.Type, actor.ID, correlation, reason).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = q.QueryRow(ctx, `
			select id from points.cp_transactions where operation_key = $1`, key).Scan(&id)
		return id, false, err
	}
	return id, err == nil, err
}

func postCP(ctx context.Context, q querier, txID, lotID uuid.UUID, bucket string, delta int64) error {
	_, err := q.Exec(ctx, `
		insert into points.cp_postings (transaction_id, lot_id, bucket, delta)
		values ($1, $2, $3, $4)`, txID, lotID, bucket, delta)
	return err
}

// moveCP moves n units of one lot from one bucket to another and posts it.
// The caller holds the lot's row lock.
func moveCP(ctx context.Context, q querier, txID, lotID uuid.UUID, from, to string, n int64) error {
	if n <= 0 {
		return fmt.Errorf("competition points: move of %d units", n)
	}
	if !cpBuckets[from] || !cpBuckets[to] || from == to {
		return fmt.Errorf("competition points: bad move %s -> %s", from, to)
	}
	tag, err := q.Exec(ctx, fmt.Sprintf(`
		update points.cp_lots
		set %[1]s = %[1]s - $2, %[2]s = %[2]s + $2, updated_at = now()
		where id = $1 and %[1]s >= $2`, from, to), lotID, n)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errCPBucketShort
	}
	if err := postCP(ctx, q, txID, lotID, from, -n); err != nil {
		return err
	}
	return postCP(ctx, q, txID, lotID, to, n)
}

// CPGrant is one new lot.
type CPGrant struct {
	CustomerID   uuid.UUID
	Source       string
	SourceID     uuid.UUID
	PolicyID     *uuid.UUID
	Quantity     int64
	ExpiresAt    *time.Time
	Restrictions map[string]any
	RestoresLot  *uuid.UUID
	Key          string
	Actor        CPActor
	Reason       *string
}

// grantCPLot issues a lot, once per source. A second call for the same
// source returns the lot the first one issued.
func grantCPLot(ctx context.Context, q querier, g CPGrant) (lotID uuid.UUID, created bool, err error) {
	if g.Quantity <= 0 {
		return uuid.Nil, false, fmt.Errorf("competition points: grant of %d", g.Quantity)
	}
	restrictions := []byte("{}")
	if len(g.Restrictions) > 0 {
		if restrictions, err = json.Marshal(g.Restrictions); err != nil {
			return uuid.Nil, false, err
		}
	}
	err = q.QueryRow(ctx, `
		insert into points.cp_lots
			(customer_id, source, source_id, policy_id, original, available, expires_at,
			 restrictions, restores_lot_id)
		values ($1, $2, $3, $4, $5, $5, $6, $7, $8)
		on conflict (source, source_id) do nothing
		returning id`,
		g.CustomerID, g.Source, g.SourceID, g.PolicyID, g.Quantity, g.ExpiresAt, restrictions, g.RestoresLot).
		Scan(&lotID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = q.QueryRow(ctx, `
			select id from points.cp_lots where source = $1 and source_id = $2`, g.Source, g.SourceID).
			Scan(&lotID)
		return lotID, false, err
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	kind := "grant"
	if g.RestoresLot != nil {
		kind = "restore"
	}
	txID, first, err := beginCPTx(ctx, q, g.Key, kind, g.CustomerID, g.Actor, &lotID, g.Reason)
	if err != nil {
		return uuid.Nil, false, err
	}
	if !first {
		return uuid.Nil, false, fmt.Errorf("competition points: operation %s already recorded for another lot", g.Key)
	}
	if err := postCP(ctx, q, txID, lotID, "issuance", -g.Quantity); err != nil {
		return uuid.Nil, false, err
	}
	if err := postCP(ctx, q, txID, lotID, "available", g.Quantity); err != nil {
		return uuid.Nil, false, err
	}
	return lotID, true, nil
}

// cpLotRow is a lot locked for a spending decision.
type cpLotRow struct {
	ID        uuid.UUID
	Source    string
	Available int64
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// lockSpendableLots locks the customer's lots that may pay for something
// right now: unexpired by the database clock, from an allowed source, with
// units available. Rows are locked in id order, the same order every
// spender and the expiry job use, so two of them cannot deadlock.
func lockSpendableLots(ctx context.Context, q querier, customerID uuid.UUID, sources []string) ([]cpLotRow, error) {
	rows, err := q.Query(ctx, `
		select id, source, available, expires_at, created_at
		from points.cp_lots
		where customer_id = $1 and available > 0 and source = any($2)
		  and (expires_at is null or expires_at > now() or grace_until > now())
		order by id
		for update`, customerID, sources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cpLotRow
	for rows.Next() {
		var l cpLotRow
		if err := rows.Scan(&l.ID, &l.Source, &l.Available, &l.ExpiresAt, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// spendOrder is the order lots are used in: soonest expiry first, so a
// customer never loses points they could have spent; then oldest.
func spendOrder(lots []cpLotRow) {
	sort.SliceStable(lots, func(i, j int) bool {
		a, b := lots[i], lots[j]
		switch {
		case a.ExpiresAt != nil && b.ExpiresAt == nil:
			return true
		case a.ExpiresAt == nil && b.ExpiresAt != nil:
			return false
		case a.ExpiresAt != nil && b.ExpiresAt != nil && !a.ExpiresAt.Equal(*b.ExpiresAt):
			return a.ExpiresAt.Before(*b.ExpiresAt)
		case !a.CreatedAt.Equal(b.CreatedAt):
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID.String() < b.ID.String()
	})
}

// CPAllocation is how much of one lot an entry holds.
type CPAllocation struct {
	LotID    uuid.UUID
	Quantity int64
}

// allocateCP picks lots to cover cost. It returns ErrCPInsufficient when
// the eligible lots do not add up to it.
func allocateCP(lots []cpLotRow, cost int64) ([]CPAllocation, error) {
	spendOrder(lots)
	var out []CPAllocation
	left := cost
	for _, l := range lots {
		if left == 0 {
			break
		}
		take := min(l.Available, left)
		if take <= 0 {
			continue
		}
		out = append(out, CPAllocation{LotID: l.ID, Quantity: take})
		left -= take
	}
	if left > 0 {
		return nil, ErrCPInsufficient
	}
	return out, nil
}

// lockLots locks the given lots in id order.
func lockLots(ctx context.Context, q querier, ids []uuid.UUID) error {
	sorted := append([]uuid.UUID(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].String() < sorted[j].String() })
	rows, err := q.Query(ctx, `
		select id from points.cp_lots where id = any($1) order by id for update`, sorted)
	if err != nil {
		return err
	}
	rows.Close()
	return rows.Err()
}
