package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPasswordResetNotFound is no code stored for this account and purpose.
var ErrPasswordResetNotFound = errors.New("password reset code not found")

// PasswordResetCode is the live code for one account and one purpose.
type PasswordResetCode struct {
	Hash      string
	ExpiresAt time.Time
	SentAt    time.Time
	Attempts  int
}

type PasswordResetRepository struct {
	db *pgxpool.Pool
}

func NewPasswordResetRepository(db *pgxpool.Pool) *PasswordResetRepository {
	return &PasswordResetRepository{db: db}
}

// Save replaces any code already stored for this account and purpose.
func (r *PasswordResetRepository) Save(ctx context.Context, subjectType string, subjectID uuid.UUID, purpose, hash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		insert into core.password_reset_codes (subject_type, subject_id, purpose, code_hash, expires_at)
		values ($1, $2, $3, $4, $5)
		on conflict (subject_type, subject_id, purpose) do update
		set code_hash = excluded.code_hash,
		    expires_at = excluded.expires_at,
		    sent_at = now(),
		    attempts = 0`,
		subjectType, subjectID, purpose, hash, expiresAt)
	return err
}

func (r *PasswordResetRepository) Get(ctx context.Context, subjectType string, subjectID uuid.UUID, purpose string) (*PasswordResetCode, error) {
	var code PasswordResetCode
	err := r.db.QueryRow(ctx, `
		select code_hash, expires_at, sent_at, attempts
		from core.password_reset_codes
		where subject_type = $1 and subject_id = $2 and purpose = $3`,
		subjectType, subjectID, purpose).Scan(&code.Hash, &code.ExpiresAt, &code.SentAt, &code.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPasswordResetNotFound
	}
	if err != nil {
		return nil, err
	}
	return &code, nil
}

func (r *PasswordResetRepository) AddAttempt(ctx context.Context, subjectType string, subjectID uuid.UUID, purpose string) (int, error) {
	var attempts int
	err := r.db.QueryRow(ctx, `
		update core.password_reset_codes
		set attempts = attempts + 1
		where subject_type = $1 and subject_id = $2 and purpose = $3
		returning attempts`,
		subjectType, subjectID, purpose).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrPasswordResetNotFound
	}
	return attempts, err
}

func (r *PasswordResetRepository) Delete(ctx context.Context, subjectType string, subjectID uuid.UUID, purpose string) error {
	_, err := r.db.Exec(ctx, `
		delete from core.password_reset_codes
		where subject_type = $1 and subject_id = $2 and purpose = $3`,
		subjectType, subjectID, purpose)
	return err
}
