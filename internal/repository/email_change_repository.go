package repository

import (
	"context"                         // request-scoped cancellation/timeouts, passed to every DB call
	"errors"                          // errors.New / errors.Is
	"github.com/google/uuid"          // UUID type for customer IDs
	"github.com/jackc/pgx/v5"         // core pgx; used for pgx.ErrNoRows
	"github.com/jackc/pgx/v5/pgxpool" // connection pool
	"time"                            // time.Time for expiry timestamps
)

var (
	ErrEmailChangeNotFound = errors.New("email change request not found")
	ErrUndoNotFound        = errors.New("undo link is not valid")
)

type EmailChangeRequest struct {
	NewEmail  string    // the address the customer wants to switch to
	CodeHash  string    // hash of the verification code (the plain code is never stored)
	ExpiresAt time.Time // when the code stops being valid
	SentAt    time.Time // when the code was last sent (useful for resend throttling)
	Attempts  int       // how many wrong guesses so far
}

// memory shape of pending email-change request
type EmailChangeRepository struct{ db *pgxpool.Pool }

func NewEmailChangeRepository(db *pgxpool.Pool) *EmailChangeRepository {
	return &EmailChangeRepository{db: db}
}

// Save replaces the pending request and resets its attempts.
func (r *EmailChangeRepository) Save(ctx context.Context, customerID uuid.UUID, newEmail, hash, ip string, expires time.Time) error {
	_, err := r.db.Exec(ctx, `
		insert into customer.email_change_requests (customer_id, new_email, code_hash, expires_at, requested_ip)
		values ($1, $2, $3, $4, $5)
		on conflict (customer_id) do update set
		    new_email = excluded.new_email, code_hash = excluded.code_hash,
		    expires_at = excluded.expires_at, requested_ip = excluded.requested_ip,
		    sent_at = now(), attempts = 0`,
		customerID, newEmail, hash, expires, ip)
	return err
}

func (r *EmailChangeRepository) Get(ctx context.Context, customerID uuid.UUID) (*EmailChangeRequest, error) {
	q := &EmailChangeRequest{}
	err := r.db.QueryRow(ctx, `
		select new_email::text, code_hash, expires_at, sent_at, attempts
		from customer.email_change_requests where customer_id = $1`, customerID,
	).Scan(&q.NewEmail, &q.CodeHash, &q.ExpiresAt, &q.SentAt, &q.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEmailChangeNotFound
	}
	return q, err
}

func (r *EmailChangeRepository) AddAttempt(ctx context.Context, customerID uuid.UUID) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		update customer.email_change_requests set attempts = attempts + 1
		where customer_id = $1 returning attempts`, customerID).Scan(&n)
	return n, err
}

// CountSince is how many changes the customer completed since the given time.
func (r *EmailChangeRepository) CountSince(ctx context.Context, customerID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		select count(*) from customer.email_history
		where customer_id = $1 and changed_at >= $2`, customerID, since).Scan(&n)
	return n, err
}

// ApplyInput is everything the swap needs.
type ApplyInput struct {
	CustomerID uuid.UUID
	NewEmail   string
	IP         string
	UndoHash   string
	UndoUntil  time.Time
	LockUntil  time.Time
	Flagged    bool
	FlagReason string
}

// Apply swaps the email in one transaction and returns the old one.
func (r *EmailChangeRepository) Apply(ctx context.Context, in ApplyInput) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var old string
	err = tx.QueryRow(ctx, `
		select email::text from customer.customers
		where id = $1 and deleted_at is null for update`, in.CustomerID).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCustomerNotFound
	}
	if err != nil {
		return "", err
	}

	_, err = tx.Exec(ctx, `
		update customer.customers set
		    email = $2, email_verified_at = now(), email_changed_at = now(),
		    redemption_locked_until = $3,
		    sessions_valid_after = date_trunc('second', now()),
		    updated_at = now()
		where id = $1`, in.CustomerID, in.NewEmail, in.LockUntil)
	if err != nil {
		return "", mapCustomerWriteError(err) // unique violation -> ErrCustomerDuplicate
	}
	_, err = tx.Exec(ctx, `
		insert into customer.email_history
		    (customer_id, old_email, new_email, changed_ip, undo_token_hash, undo_expires_at, flagged, flag_reason)
		values ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''))`,
		in.CustomerID, old, in.NewEmail, in.IP, in.UndoHash, in.UndoUntil, in.Flagged, in.FlagReason)
	if err != nil {
		return "", err
	}
	// Codes that were sent to the old address must not work any more.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`delete from customer.email_change_requests where customer_id = $1`, []any{in.CustomerID}},
		{`delete from core.password_reset_codes where subject_type = 'customer' and subject_id = $1`, []any{in.CustomerID}},
		{`delete from core.login_codes where channel = 'email' and destination = $1`, []any{old}},
	} {
		if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
			return "", err
		}
	}
	return old, tx.Commit(ctx)
}

// Revert restores the address the account had before the change the token
// belongs to, and rolls back every later change with it. That keeps a second
// change from defeating the first one's undo link.
func (r *EmailChangeRepository) Revert(ctx context.Context, tokenHash string, lockUntil time.Time) (uuid.UUID, string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, "", err
	}
	defer tx.Rollback(ctx)

	var customerID uuid.UUID
	var oldEmail string
	var changedAt time.Time
	err = tx.QueryRow(ctx, `
		select customer_id, old_email::text, changed_at
		from customer.email_history
		where undo_token_hash = $1 and status = 'active' and undo_expires_at > now()
		for update`, tokenHash).Scan(&customerID, &oldEmail, &changedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ErrUndoNotFound
	}
	if err != nil {
		return uuid.Nil, "", err
	}

	_, err = tx.Exec(ctx, `
		update customer.customers set
		    email = $2, redemption_locked_until = $3,
		    password_change_required = true,
		    sessions_valid_after = date_trunc('second', now()), updated_at = now()
		where id = $1 and deleted_at is null`, customerID, oldEmail, lockUntil)
	if err != nil {
		return uuid.Nil, "", mapCustomerWriteError(err)
	}
	_, err = tx.Exec(ctx, `
		update customer.email_history set
		    status = 'reverted', reverted_at = now(), flagged = true,
		    flag_reason = coalesce(flag_reason, 'reverted by account owner')
		where customer_id = $1 and status = 'active' and changed_at >= $2`,
		customerID, changedAt)
	if err != nil {
		return uuid.Nil, "", err
	}
	// Whatever the person who made the change left behind goes with it.
	for _, q := range []string{
		`delete from customer.email_change_requests where customer_id = $1`,
		`delete from core.password_reset_codes where subject_type = 'customer' and subject_id = $1`,
	} {
		if _, err := tx.Exec(ctx, q, customerID); err != nil {
			return uuid.Nil, "", err
		}
	}
	if _, err := tx.Exec(ctx, `
		delete from customer.social_identities
		where customer_id = $1 and created_at >= $2`, customerID, changedAt); err != nil {
		return uuid.Nil, "", err
	}
	return customerID, oldEmail, tx.Commit(ctx)
}

// EmailInUse reports whether any customer row, deleted or not, holds the email.
func (r *EmailChangeRepository) EmailInUse(ctx context.Context, email string) (bool, error) {
	var ok bool
	err := r.db.QueryRow(ctx, `
		select exists(select 1 from customer.customers where email = $1)`, email).Scan(&ok)
	return ok, err
}
