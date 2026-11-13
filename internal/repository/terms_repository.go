package repository

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TermsVersionUnspecified is recorded when an older client sends no version.
const TermsVersionUnspecified = "unspecified"

// NormaliseTermsVersion trims a client's terms version and caps it at 64
// characters. A blank one becomes TermsVersionUnspecified.
func NormaliseTermsVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return TermsVersionUnspecified
	}
	if utf8.RuneCountInString(v) > 64 {
		v = string([]rune(v)[:64])
	}
	return v
}

func recordTermsAcceptance(ctx context.Context, db *pgxpool.Pool, actorType string, actorID uuid.UUID, document, version string, countryID *uuid.UUID) error {
	_, err := db.Exec(ctx, `
		insert into core.terms_acceptances (actor_type, actor_id, document, version, country_id)
		values ($1, $2, $3, $4, $5)`,
		actorType, actorID, document, NormaliseTermsVersion(version), countryID)
	return err
}

// RecordTermsAcceptance keeps the customer terms version a customer agreed
// to when their account was made.
func (r *CustomerRepository) RecordTermsAcceptance(ctx context.Context, customerID uuid.UUID, version string, countryID uuid.UUID) error {
	return recordTermsAcceptance(ctx, r.db, "customer", customerID, "customer_terms", version, &countryID)
}

// RecordTermsAcceptance keeps the seller agreement version a seller agreed to.
func (r *SellerRepository) RecordTermsAcceptance(ctx context.Context, sellerID uuid.UUID, version string, countryID uuid.UUID) error {
	return recordTermsAcceptance(ctx, r.db, "seller", sellerID, "seller_terms", version, &countryID)
}
