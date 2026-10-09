package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxSMS is one text message waiting in core.sms_outbox.
type OutboxSMS struct {
	ID        uuid.UUID
	Kind      string
	DedupeKey string
	ToPhone   string
	Body      string
	Attempts  int
}

type SMSRepository struct {
	db *pgxpool.Pool
}

func NewSMSRepository(db *pgxpool.Pool) *SMSRepository {
	return &SMSRepository{db: db}
}

// Enqueue writes a message to the outbox and reports whether it was new.
func (r *SMSRepository) Enqueue(ctx context.Context, m *OutboxSMS) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		insert into core.sms_outbox (kind, dedupe_key, to_phone, body)
		values ($1, $2, $3, $4)
		on conflict (dedupe_key) do nothing`,
		m.Kind, m.DedupeKey, m.ToPhone, m.Body)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Due returns up to limit messages whose turn to send has come, oldest first.
func (r *SMSRepository) Due(ctx context.Context, limit int) ([]OutboxSMS, error) {
	rows, err := r.db.Query(ctx, `
		select id, kind, dedupe_key, to_phone, body, attempts
		from core.sms_outbox
		where status = 'pending' and next_attempt_at <= now()
		order by next_attempt_at
		limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []OutboxSMS{}
	for rows.Next() {
		var m OutboxSMS
		if err := rows.Scan(&m.ID, &m.Kind, &m.DedupeKey, &m.ToPhone, &m.Body, &m.Attempts); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *SMSRepository) MarkSent(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update core.sms_outbox
		set status = 'sent', sent_at = now(), attempts = attempts + 1, last_error = null
		where id = $1`, id)
	return err
}

// MarkAttemptFailed records a failed send. With a retry time the message
// waits for it; without one it is given up on.
func (r *SMSRepository) MarkAttemptFailed(ctx context.Context, id uuid.UUID, reason string, retryAt *time.Time) error {
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	_, err := r.db.Exec(ctx, `
		update core.sms_outbox
		set attempts = attempts + 1,
		    last_error = $2,
		    status = case when $3::timestamptz is null then 'failed' else 'pending' end,
		    next_attempt_at = coalesce($3::timestamptz, next_attempt_at)
		where id = $1`, id, reason, retryAt)
	return err
}
