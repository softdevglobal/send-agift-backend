package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key in progress")
	ErrIdempotencyNotFound = errors.New("idempotency key not found")
)

const idempotencyScopeShipmentLabel = "shipment_label"

// staleProcessingAfter lets a crashed BuyLabel retry the same key.
const staleProcessingAfter = 30 * time.Second

type IdempotencyRepository struct {
	db *pgxpool.Pool
}

func NewIdempotencyRepository(db *pgxpool.Pool) *IdempotencyRepository {
	return &IdempotencyRepository{db: db}
}

// Acquire tries to start an idempotent operation. When skip is true, cached holds the prior response.
func (r *IdempotencyRepository) Acquire(ctx context.Context, scope, key string) (cached json.RawMessage, skip bool, err error) {
	tag, err := r.db.Exec(ctx, `
		insert into core.idempotency_keys (key, scope, status)
		values ($1, $2, 'processing')
		on conflict (key) do nothing`, key, scope)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 1 {
		return nil, false, nil
	}

	var status string
	var response json.RawMessage
	var updatedAt time.Time
	err = r.db.QueryRow(ctx, `
		select status, response_body, updated_at from core.idempotency_keys
		where key = $1`, key,
	).Scan(&status, &response, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrIdempotencyNotFound
	}
	if err != nil {
		return nil, false, err
	}
	switch status {
	case "completed":
		return response, true, nil
	case "failed":
		// Previous attempt failed — reclaim for a clean retry.
		_, err = r.db.Exec(ctx, `
			update core.idempotency_keys
			set status = 'processing', response_body = null, updated_at = now()
			where key = $1 and status = 'failed'`, key)
		return nil, false, err
	case "processing":
		// Crashed / abandoned attempt — reclaim after a short grace period.
		if time.Since(updatedAt) >= staleProcessingAfter {
			tag, err = r.db.Exec(ctx, `
				update core.idempotency_keys
				set status = 'processing', response_body = null, updated_at = now()
				where key = $1 and status = 'processing' and updated_at <= $2`,
				key, time.Now().UTC().Add(-staleProcessingAfter))
			if err != nil {
				return nil, false, err
			}
			if tag.RowsAffected() == 1 {
				return nil, false, nil
			}
		}
		return nil, false, ErrIdempotencyConflict
	default:
		return nil, false, nil
	}
}

func (r *IdempotencyRepository) Complete(ctx context.Context, scope, key string, response json.RawMessage) error {
	_, err := r.db.Exec(ctx, `
		update core.idempotency_keys
		set status = 'completed', response_body = $2, updated_at = now()
		where key = $1 and scope = $3`, key, response, scope)
	return err
}

// Fail marks a started key so the same idempotency_key can be retried.
func (r *IdempotencyRepository) Fail(ctx context.Context, scope, key string) error {
	_, err := r.db.Exec(ctx, `
		update core.idempotency_keys
		set status = 'failed', updated_at = now()
		where key = $1 and scope = $2 and status = 'processing'`, key, scope)
	return err
}

func ShipmentLabelScope() string { return idempotencyScopeShipmentLabel }
