package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLoginCodeNotFound = errors.New("login code not found")

// LoginCode is the live code for one email or phone and purpose. SendCount
// and Attempts count from WindowStart, so a resend brings no new guesses.
type LoginCode struct {
	CustomerID  *uuid.UUID
	CodeHash    string
	ExpiresAt   time.Time
	SentAt      time.Time
	WindowStart time.Time
	SendCount   int
	Attempts    int
}

type LoginCodeRepository struct {
	db *pgxpool.Pool
}

func NewLoginCodeRepository(db *pgxpool.Pool) *LoginCodeRepository {
	return &LoginCodeRepository{db: db}
}

func (r *LoginCodeRepository) Get(ctx context.Context, channel, destination, purpose string) (*LoginCode, error) {
	c := &LoginCode{}
	err := r.db.QueryRow(ctx, `
		select customer_id, code_hash, expires_at, sent_at, window_start, send_count, attempts
		from core.login_codes
		where channel = $1 and destination = $2 and purpose = $3`,
		channel, destination, purpose,
	).Scan(&c.CustomerID, &c.CodeHash, &c.ExpiresAt, &c.SentAt, &c.WindowStart, &c.SendCount, &c.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrLoginCodeNotFound
	}
	return c, err
}

// Save replaces the live code. The day's counters carry over until the
// window is a day old.
func (r *LoginCodeRepository) Save(ctx context.Context, channel, destination, purpose string, customerID *uuid.UUID, hash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		insert into core.login_codes as lc (channel, destination, purpose, customer_id, code_hash, expires_at)
		values ($1, $2, $3, $4, $5, $6)
		on conflict (channel, destination, purpose) do update set
		    customer_id  = excluded.customer_id,
		    code_hash    = excluded.code_hash,
		    expires_at   = excluded.expires_at,
		    sent_at      = now(),
		    window_start = case when lc.window_start < now() - interval '1 day' then now() else lc.window_start end,
		    send_count   = case when lc.window_start < now() - interval '1 day' then 1 else lc.send_count + 1 end,
		    attempts     = case when lc.window_start < now() - interval '1 day' then 0 else lc.attempts end`,
		channel, destination, purpose, customerID, hash, expiresAt)
	return err
}

func (r *LoginCodeRepository) AddAttempt(ctx context.Context, channel, destination, purpose string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		update core.login_codes set attempts = attempts + 1
		where channel = $1 and destination = $2 and purpose = $3
		returning attempts`, channel, destination, purpose).Scan(&n)
	return n, err
}

// Consume spends the code so it works once. False means another request
// spent it first.
func (r *LoginCodeRepository) Consume(ctx context.Context, channel, destination, purpose, hash string) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		update core.login_codes set code_hash = ''
		where channel = $1 and destination = $2 and purpose = $3
		  and code_hash = $4 and code_hash <> ''`, channel, destination, purpose, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
