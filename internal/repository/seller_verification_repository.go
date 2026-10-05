package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
)

// SellerEmailCode is where a seller's email confirmation stands.
type SellerEmailCode struct {
	SellerID           uuid.UUID
	Email              string
	LegalName          string
	TradingName        *string
	VerificationStatus string
	EmailVerifiedAt    *time.Time
	CodeHash           *string
	ExpiresAt          *time.Time
	SentAt             *time.Time
	Attempts           int
}

// GetEmailCode loads the confirmation state of an active seller by email.
func (r *SellerRepository) GetEmailCode(ctx context.Context, email string) (*SellerEmailCode, error) {
	c := &SellerEmailCode{}
	err := r.db.QueryRow(ctx, `
		select id, email, legal_name, trading_name, verification_status, email_verified_at,
		       email_code_hash, email_code_expires_at, email_code_sent_at, email_code_attempts
		from seller.sellers
		where email = $1 and status = 'active'`, email,
	).Scan(&c.SellerID, &c.Email, &c.LegalName, &c.TradingName, &c.VerificationStatus, &c.EmailVerifiedAt,
		&c.CodeHash, &c.ExpiresAt, &c.SentAt, &c.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSellerNotFound
	}
	return c, err
}

// SetEmailCode stores a new confirmation code hash and resets the attempts.
func (r *SellerRepository) SetEmailCode(ctx context.Context, sellerID uuid.UUID, hash string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		update seller.sellers
		set email_code_hash = $2, email_code_expires_at = $3, email_code_sent_at = now(),
		    email_code_attempts = 0, updated_at = now()
		where id = $1`, sellerID, hash, expiresAt)
	return err
}

// CountEmailCodeAttempt records one wrong code.
func (r *SellerRepository) CountEmailCodeAttempt(ctx context.Context, sellerID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update seller.sellers set email_code_attempts = email_code_attempts + 1
		where id = $1`, sellerID)
	return err
}

// MarkEmailVerified confirms the seller's email and moves them to admin
// review. It reports false when another request confirmed it first.
func (r *SellerRepository) MarkEmailVerified(ctx context.Context, sellerID uuid.UUID) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		update seller.sellers
		set email_verified_at = now(),
		    verification_status = 'pending',
		    email_code_hash = null, email_code_expires_at = null, email_code_attempts = 0,
		    updated_at = now()
		where id = $1 and email_verified_at is null`, sellerID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// VerificationStatus returns an active seller's verification status.
func (r *SellerRepository) VerificationStatus(ctx context.Context, sellerID string) (string, error) {
	var status string
	err := r.db.QueryRow(ctx, `
		select verification_status from seller.sellers
		where id = $1 and status = 'active'`, sellerID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSellerNotFound
	}
	return status, err
}

// AdminSellerFilter narrows the admin seller list.
type AdminSellerFilter struct {
	VerificationStatus string // empty = any
	Query              string // name or email contains
	Limit              int
	Offset             int
}

// AdminList returns sellers for review, those waiting longest first, and the
// total that match.
func (r *SellerRepository) AdminList(ctx context.Context, f AdminSellerFilter) ([]models.AdminSellerSummary, int, error) {
	q := "%" + strings.ToLower(strings.TrimSpace(f.Query)) + "%"
	rows, err := r.db.Query(ctx, `
		select s.id, s.country_id, s.seller_type, s.legal_name, s.trading_name, s.email, s.phone,
		       s.verification_status, s.status, s.created_at, s.updated_at, s.image_url,
		       s.email_verified_at, s.verification_note, s.verification_reviewed_at,
		       coalesce(c.name, ''),
		       (select count(*) from seller.shops sh where sh.seller_id = s.id),
		       (select a.city from seller.seller_addresses a where a.seller_id = s.id
		         order by a.is_default desc, a.created_at limit 1),
		       count(*) over ()
		from seller.sellers s
		left join core.countries c on c.id = s.country_id
		where s.status = 'active'
		  and ($1 = '' or s.verification_status = $1)
		  and ($2 = '%%' or lower(s.legal_name) like $2 or lower(coalesce(s.trading_name, '')) like $2
		       or lower(s.email::text) like $2)
		order by case s.verification_status when 'pending' then 0 when 'unverified' then 1 else 2 end,
		         s.created_at asc
		limit $3 offset $4`, f.VerificationStatus, q, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []models.AdminSellerSummary{}
	total := 0
	for rows.Next() {
		var s models.AdminSellerSummary
		if err := rows.Scan(
			&s.ID, &s.CountryID, &s.SellerType, &s.LegalName, &s.TradingName, &s.Email, &s.Phone,
			&s.VerificationStatus, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.ImageURL,
			&s.EmailVerifiedAt, &s.VerificationNote, &s.VerificationReviewedAt,
			&s.CountryName, &s.ShopCount, &s.City, &total,
		); err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// CountByVerification returns how many active sellers are in each status.
func (r *SellerRepository) CountByVerification(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.Query(ctx, `
		select verification_status, count(*) from seller.sellers
		where status = 'active' group by verification_status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{"unverified": 0, "pending": 0, "verified": 0, "rejected": 0}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

// SetVerification records an admin's decision on a seller whose email is
// confirmed. It returns ErrSellerNotFound for an unknown seller or one still
// confirming their email.
func (r *SellerRepository) SetVerification(ctx context.Context, sellerID, status string, note *string, adminID *uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `
		update seller.sellers
		set verification_status = $2, verification_note = $3,
		    verification_reviewed_at = now(), verification_reviewed_by = $4, updated_at = now()
		where id = $1 and status = 'active' and email_verified_at is not null`,
		sellerID, status, note, adminID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSellerNotFound
	}
	return nil
}
