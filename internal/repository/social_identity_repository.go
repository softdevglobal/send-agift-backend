package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrSocialIdentityNotFound is a provider account not linked to anyone yet.
var ErrSocialIdentityNotFound = errors.New("social identity not found")

// FindSocialIdentity returns the customer a provider account is linked to.
func (r *CustomerRepository) FindSocialIdentity(ctx context.Context, provider, subject string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `
		update customer.social_identities set last_used_at = now()
		where provider = $1 and subject = $2
		returning customer_id`, provider, subject).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrSocialIdentityNotFound
	}
	return id, err
}

// LinkSocialIdentity links a provider account to a customer. Linking the
// same account again is a no-op.
func (r *CustomerRepository) LinkSocialIdentity(ctx context.Context, customerID uuid.UUID, provider, subject, email string) error {
	_, err := r.db.Exec(ctx, `
		insert into customer.social_identities (customer_id, provider, subject, email)
		values ($1, $2, $3, nullif($4, ''))
		on conflict (provider, subject) do update set last_used_at = now()`,
		customerID, provider, subject, email)
	return err
}
