package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"myapp/internal/models"
)

// ErrSellerApplicationNotFound is a seller with no application on file, such
// as one who signed up before applications existed.
var ErrSellerApplicationNotFound = errors.New("seller application not found")

// CreateApplication stores a seller's application.
func (r *SellerRepository) CreateApplication(ctx context.Context, a *models.SellerApplication) error {
	sections := []any{a.Business, a.Representative, a.Addresses, a.Shop, a.Fulfilment, a.Payout, a.Consents}
	encoded := make([]any, len(sections))
	for i, s := range sections {
		b, err := json.Marshal(s)
		if err != nil {
			return err
		}
		encoded[i] = b
	}
	if a.ReviewReasons == nil {
		a.ReviewReasons = []string{}
	}
	return r.db.QueryRow(ctx, `
		insert into seller.seller_applications
		    (seller_id, business, representative, addresses, shop, fulfilment, payout, consents, review_reasons)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		returning submitted_at, updated_at`,
		a.SellerID, encoded[0], encoded[1], encoded[2], encoded[3], encoded[4], encoded[5], encoded[6], a.ReviewReasons,
	).Scan(&a.SubmittedAt, &a.UpdatedAt)
}

// GetApplication loads a seller's application.
func (r *SellerRepository) GetApplication(ctx context.Context, sellerID uuid.UUID) (*models.SellerApplication, error) {
	a := &models.SellerApplication{SellerID: sellerID}
	var business, representative, addresses, shop, fulfilment, payout, consents []byte
	var docName, docType *string
	var docAt *time.Time
	err := r.db.QueryRow(ctx, `
		select business, representative, addresses, shop, fulfilment, payout, consents, review_reasons,
		       document_name, document_content_type, document_uploaded_at, submitted_at, updated_at
		from seller.seller_applications where seller_id = $1`, sellerID,
	).Scan(&business, &representative, &addresses, &shop, &fulfilment, &payout, &consents, &a.ReviewReasons,
		&docName, &docType, &docAt, &a.SubmittedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSellerApplicationNotFound
	}
	if err != nil {
		return nil, err
	}
	for _, s := range []struct {
		raw []byte
		dst any
	}{
		{business, &a.Business}, {representative, &a.Representative}, {addresses, &a.Addresses},
		{shop, &a.Shop}, {fulfilment, &a.Fulfilment}, {payout, &a.Payout}, {consents, &a.Consents},
	} {
		if err := json.Unmarshal(s.raw, s.dst); err != nil {
			return nil, err
		}
	}
	if docName != nil && docAt != nil {
		a.Document = &models.ApplicationDocument{Name: *docName, ContentType: derefString(docType), UploadedAt: *docAt}
	}
	return a, nil
}

// SetApplicationDocument records the business registration evidence a seller
// uploaded, and returns the key of any document it replaces.
func (r *SellerRepository) SetApplicationDocument(ctx context.Context, sellerID uuid.UUID, key, name, contentType string) (*string, error) {
	var previous *string
	err := r.db.QueryRow(ctx, `
		with old as (select document_key from seller.seller_applications where seller_id = $1)
		update seller.seller_applications
		set document_key = $2, document_name = $3, document_content_type = $4,
		    document_uploaded_at = now(), updated_at = now()
		where seller_id = $1
		returning (select document_key from old)`, sellerID, key, name, contentType,
	).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSellerApplicationNotFound
	}
	return previous, err
}

// ApplicationDocumentKey is where a seller's business document is stored.
func (r *SellerRepository) ApplicationDocumentKey(ctx context.Context, sellerID uuid.UUID) (*string, error) {
	var key *string
	err := r.db.QueryRow(ctx, `select document_key from seller.seller_applications where seller_id = $1`, sellerID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSellerApplicationNotFound
	}
	return key, err
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
