package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key in progress")
	ErrIdempotencyNotFound = errors.New("idempotency key not found")
)

const idempotencyScopeShipmentLabel = "shipment_label"

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
	err = r.db.QueryRow(ctx, `
		select status, response_body from core.idempotency_keys
		where key = $1`, key,
	).Scan(&status, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrIdempotencyNotFound
	}
	if err != nil {
		return nil, false, err
	}
	switch status {
	case "completed":
		return response, true, nil
	case "processing":
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

func ShipmentLabelScope() string { return idempotencyScopeShipmentLabel }
