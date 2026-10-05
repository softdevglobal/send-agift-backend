package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxEmail is one rendered email waiting in core.email_outbox.
type OutboxEmail struct {
	ID        uuid.UUID
	Kind      string
	DedupeKey string
	ToEmail   string
	ToName    string
	Subject   string
	HTMLBody  string
	TextBody  string
	Attempts  int
}

type EmailRepository struct {
	db *pgxpool.Pool
}

func NewEmailRepository(db *pgxpool.Pool) *EmailRepository {
	return &EmailRepository{db: db}
}

// Enqueue writes an email to the outbox and reports whether it was new. An
// email with the same dedupe key is never queued twice.
func (r *EmailRepository) Enqueue(ctx context.Context, e *OutboxEmail) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		insert into core.email_outbox (kind, dedupe_key, to_email, to_name, subject, html_body, text_body)
		values ($1, $2, $3, nullif($4, ''), $5, $6, $7)
		on conflict (dedupe_key) do nothing`,
		e.Kind, e.DedupeKey, e.ToEmail, e.ToName, e.Subject, e.HTMLBody, e.TextBody)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Due returns up to limit emails whose turn to send has come, oldest first.
func (r *EmailRepository) Due(ctx context.Context, limit int) ([]OutboxEmail, error) {
	rows, err := r.db.Query(ctx, `
		select id, kind, dedupe_key, to_email, coalesce(to_name, ''), subject, html_body, text_body, attempts
		from core.email_outbox
		where status = 'pending' and next_attempt_at <= now()
		order by next_attempt_at
		limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []OutboxEmail{}
	for rows.Next() {
		var e OutboxEmail
		if err := rows.Scan(&e.ID, &e.Kind, &e.DedupeKey, &e.ToEmail, &e.ToName, &e.Subject, &e.HTMLBody, &e.TextBody, &e.Attempts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *EmailRepository) MarkSent(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update core.email_outbox
		set status = 'sent', sent_at = now(), attempts = attempts + 1, last_error = null
		where id = $1`, id)
	return err
}

// MarkAttemptFailed records a failed send. With a retry time the email waits
// for it; without one it is given up on.
func (r *EmailRepository) MarkAttemptFailed(ctx context.Context, id uuid.UUID, reason string, retryAt *time.Time) error {
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	_, err := r.db.Exec(ctx, `
		update core.email_outbox
		set attempts = attempts + 1,
		    last_error = $2,
		    status = case when $3::timestamptz is null then 'failed' else 'pending' end,
		    next_attempt_at = coalesce($3::timestamptz, next_attempt_at)
		where id = $1`, id, reason, retryAt)
	return err
}
