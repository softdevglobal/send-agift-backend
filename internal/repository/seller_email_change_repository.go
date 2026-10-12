package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SellerEmailChangeRepository stores a seller's pending email change and the
// history that makes it undoable. It mirrors EmailChangeRepository for
// customers, on the seller tables.
type SellerEmailChangeRepository struct{ db *pgxpool.Pool }

func NewSellerEmailChangeRepository(db *pgxpool.Pool) *SellerEmailChangeRepository {
	return &SellerEmailChangeRepository{db: db}
}

// Save replaces the pending request and resets its attempts.
func (r *SellerEmailChangeRepository) Save(ctx context.Context, sellerID uuid.UUID, newEmail, hash, ip string, expires time.Time) error {
	_, err := r.db.Exec(ctx, `
		insert into seller.email_change_requests (seller_id, new_email, code_hash, expires_at, requested_ip)
		values ($1, $2, $3, $4, $5)
		on conflict (seller_id) do update set
		    new_email = excluded.new_email, code_hash = excluded.code_hash,
		    expires_at = excluded.expires_at, requested_ip = excluded.requested_ip,
		    sent_at = now(), attempts = 0`,
		sellerID, newEmail, hash, expires, ip)
	return err
}

func (r *SellerEmailChangeRepository) Get(ctx context.Context, sellerID uuid.UUID) (*EmailChangeRequest, error) {
	q := &EmailChangeRequest{}
	err := r.db.QueryRow(ctx, `
		select new_email::text, code_hash, expires_at, sent_at, attempts
		from seller.email_change_requests where seller_id = $1`, sellerID,
	).Scan(&q.NewEmail, &q.CodeHash, &q.ExpiresAt, &q.SentAt, &q.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEmailChangeNotFound
	}
	return q, err
}

func (r *SellerEmailChangeRepository) AddAttempt(ctx context.Context, sellerID uuid.UUID) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		update seller.email_change_requests set attempts = attempts + 1
		where seller_id = $1 returning attempts`, sellerID).Scan(&n)
	return n, err
}

// CountSince is how many changes the seller completed since the given time.
func (r *SellerEmailChangeRepository) CountSince(ctx context.Context, sellerID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `
		select count(*) from seller.email_history
		where seller_id = $1 and changed_at >= $2`, sellerID, since).Scan(&n)
	return n, err
}

// SellerApplyInput is everything the swap needs.
type SellerApplyInput struct {
	SellerID   uuid.UUID
	NewEmail   string
	IP         string
	UndoHash   string
	UndoUntil  time.Time
	LockUntil  time.Time
	Flagged    bool
	FlagReason string
}

// Apply swaps the email in one transaction and returns the old one. The new
// address was proven by the emailed code, so it counts as confirmed.
func (r *SellerEmailChangeRepository) Apply(ctx context.Context, in SellerApplyInput) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var old string
	err = tx.QueryRow(ctx, `
		select email::text from seller.sellers
		where id = $1 and status = 'active' for update`, in.SellerID).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSellerNotFound
	}
	if err != nil {
		return "", err
	}

	_, err = tx.Exec(ctx, `
		update seller.sellers set
		    email = $2, email_verified_at = now(), email_changed_at = now(),
		    funding_locked_until = $3,
		    sessions_valid_after = date_trunc('second', now()),
		    updated_at = now()
		where id = $1`, in.SellerID, in.NewEmail, in.LockUntil)
	if err != nil {
		return "", mapSellerWriteError(err)
	}
	_, err = tx.Exec(ctx, `
		insert into seller.email_history
		    (seller_id, old_email, new_email, changed_ip, undo_token_hash, undo_expires_at, flagged, flag_reason)
		values ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''))`,
		in.SellerID, old, in.NewEmail, in.IP, in.UndoHash, in.UndoUntil, in.Flagged, in.FlagReason)
	if err != nil {
		return "", err
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`delete from seller.email_change_requests where seller_id = $1`, []any{in.SellerID}},
		{`delete from core.password_reset_codes where subject_type = 'seller' and subject_id = $1`, []any{in.SellerID}},
	} {
		if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
			return "", err
		}
	}
	return old, tx.Commit(ctx)
}

// Revert restores the address the account had before the change the token
// belongs to, and rolls back every later change with it. lockedHash replaces
// the password, which nobody knows, so signing in again goes through the
// forgotten-password code sent to the restored address.
func (r *SellerEmailChangeRepository) Revert(ctx context.Context, tokenHash, lockedHash string, lockUntil time.Time) (uuid.UUID, string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, "", err
	}
	defer tx.Rollback(ctx)

	var sellerID uuid.UUID
	var oldEmail string
	var changedAt time.Time
	err = tx.QueryRow(ctx, `
		select seller_id, old_email::text, changed_at
		from seller.email_history
		where undo_token_hash = $1 and status = 'active' and undo_expires_at > now()
		for update`, tokenHash).Scan(&sellerID, &oldEmail, &changedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ErrUndoNotFound
	}
	if err != nil {
		return uuid.Nil, "", err
	}

	_, err = tx.Exec(ctx, `
		update seller.sellers set
		    email = $2, funding_locked_until = $3, password_hash = $4,
		    sessions_valid_after = date_trunc('second', now()), updated_at = now()
		where id = $1 and status <> 'deleted'`, sellerID, oldEmail, lockUntil, lockedHash)
	if err != nil {
		return uuid.Nil, "", mapSellerWriteError(err)
	}
	_, err = tx.Exec(ctx, `
		update seller.email_history set
		    status = 'reverted', reverted_at = now(), flagged = true,
		    flag_reason = coalesce(flag_reason, 'reverted by account owner')
		where seller_id = $1 and status = 'active' and changed_at >= $2`,
		sellerID, changedAt)
	if err != nil {
		return uuid.Nil, "", err
	}
	for _, q := range []string{
		`delete from seller.email_change_requests where seller_id = $1`,
		`delete from core.password_reset_codes where subject_type = 'seller' and subject_id = $1`,
	} {
		if _, err := tx.Exec(ctx, q, sellerID); err != nil {
			return uuid.Nil, "", err
		}
	}
	return sellerID, oldEmail, tx.Commit(ctx)
}

// ─── Shared lookups on the seller repository ─────────────────────────────

// EmailInUseAnywhere reports whether any seller (deleted or not), customer
// or admin signs in with the email. Sign-in is by email alone, so one address
// must never belong to two kinds of account.
func (r *SellerRepository) EmailInUseAnywhere(ctx context.Context, email string) (bool, error) {
	var used bool
	err := r.db.QueryRow(ctx, `
		select exists(select 1 from seller.sellers where email = $1)
		    or exists(select 1 from customer.customers where email = $1)
		    or exists(select 1 from admin.admin_users where email = $1)`, email).Scan(&used)
	return used, err
}

// EmailReserved reports whether another account changed away from this email
// and can still undo it.
func (r *SellerRepository) EmailReserved(ctx context.Context, email string, except uuid.UUID) (bool, error) {
	return emailReserved(ctx, r.db, email, except)
}

// FundingLockedUntil is when buying points and raising product rewards open
// up again after an email change, or nil when nothing is locked.
func (r *SellerRepository) FundingLockedUntil(ctx context.Context, id uuid.UUID) (*time.Time, error) {
	var t *time.Time
	err := r.db.QueryRow(ctx, `
		select funding_locked_until from seller.sellers where id = $1`, id).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSellerNotFound
	}
	return t, err
}

// ─── Sessions ────────────────────────────────────────────────────────────

// SessionRepository tells when an account's older tokens stop working, for
// both customers and sellers.
type SessionRepository struct{ db *pgxpool.Pool }

func NewSessionRepository(db *pgxpool.Pool) *SessionRepository { return &SessionRepository{db: db} }

// SessionsValidAfter returns the zero time when no cut-off is set or the role
// has none.
func (r *SessionRepository) SessionsValidAfter(ctx context.Context, role, id string) (time.Time, error) {
	var query string
	switch role {
	case "customer":
		query = `select sessions_valid_after from customer.customers where id = $1`
	case "seller":
		query = `select sessions_valid_after from seller.sellers where id = $1`
	default:
		return time.Time{}, nil
	}
	var t *time.Time
	err := r.db.QueryRow(ctx, query, id).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) || t == nil {
		return time.Time{}, nil
	}
	return *t, err
}

// emailReserved is the one reservation rule: an address someone changed away
// from, in either kind of account, stays theirs while they can undo it.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func emailReserved(ctx context.Context, q rowQuerier, email string, except uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		select exists(
		    select 1 from customer.email_history
		    where old_email = $1 and status = 'active' and undo_expires_at > now()
		      and customer_id <> $2)
		    or exists(
		    select 1 from seller.email_history
		    where old_email = $1 and status = 'active' and undo_expires_at > now()
		      and seller_id <> $2)`, email, except).Scan(&ok)
	return ok, err
}
