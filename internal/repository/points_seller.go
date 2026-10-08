package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"myapp/internal/models"
)

var (
	ErrPurchaseNotFound = errors.New("points purchase not found")
	// ErrPurchaseState is a purchase that is no longer pending.
	ErrPurchaseState = errors.New("points purchase is no longer pending")
	// ErrPurchaseKeyReused is an idempotency key sent again with a different
	// amount.
	ErrPurchaseKeyReused = errors.New("idempotency key already used for a different purchase")
	// ErrPurchaseAmount is a confirmed payment too small to buy a point.
	ErrPurchaseAmount = errors.New("paid amount does not buy any points")
	// ErrProviderReferenceUsed is a payment reference already recorded
	// against another purchase: one payment can only ever buy points once.
	ErrProviderReferenceUsed = errors.New("payment reference already used by another purchase")
)

// ─── Seller wallet ────────────────────────────────────────────────────────

// SellerWallet is a seller's balance, reserved points and latest entries.
func (r *PointsRepository) SellerWallet(ctx context.Context, sellerID uuid.UUID, limit int) (*models.SellerPointsWallet, error) {
	w := &models.SellerPointsWallet{SellerID: sellerID, Entries: []models.PointsEntry{}}
	err := r.db.QueryRow(ctx, `
		select balance, reserved, lifetime_purchased, lifetime_spent
		from points.seller_points_accounts where seller_id = $1`, sellerID).
		Scan(&w.Balance, &w.Reserved, &w.LifetimePurchased, &w.LifetimeSpent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	w.Available = w.Balance - w.Reserved
	rows, err := r.db.Query(ctx, ledgerSelect+`
		where l.seller_id = $1
		order by l.seq desc
		limit $2`, sellerID, limit)
	if err != nil {
		return nil, err
	}
	w.Entries, err = scanPointsEntries(rows)
	return w, err
}

// ─── Purchases ────────────────────────────────────────────────────────────

const purchaseSelect = `
	select pp.id, pp.seller_id, pp.amount_cents, pp.currency, pp.cents_per_point, pp.points,
	       pp.paid_amount_cents, pp.points_credited, pp.status, pp.provider, pp.provider_reference,
	       pp.checkout_url, pp.failure_reason, pp.ledger_entry_id, pp.confirmed_by, pp.completed_at,
	       pp.failed_at, pp.created_at, pp.updated_at,
	       coalesce(s.trading_name, s.legal_name), s.email::text
	from points.points_purchases pp
	inner join seller.sellers s on s.id = pp.seller_id
`

func scanPurchase(row scanner) (*models.PointsPurchase, error) {
	p := &models.PointsPurchase{}
	err := row.Scan(&p.ID, &p.SellerID, &p.AmountCents, &p.Currency, &p.CentsPerPoint, &p.Points,
		&p.PaidAmountCents, &p.PointsCredited, &p.Status, &p.Provider, &p.ProviderReference,
		&p.CheckoutURL, &p.FailureReason, &p.LedgerEntryID, &p.ConfirmedBy, &p.CompletedAt,
		&p.FailedAt, &p.CreatedAt, &p.UpdatedAt, &p.SellerName, &p.SellerEmail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPurchaseNotFound
	}
	return p, err
}

func scanPurchases(rows pgx.Rows) ([]models.PointsPurchase, error) {
	defer rows.Close()
	out := []models.PointsPurchase{}
	for rows.Next() {
		p, err := scanPurchase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// CreatePurchase records a pending purchase. A retry with the same
// idempotency key gets the purchase it already made; the same key with a
// different amount is refused.
func (r *PointsRepository) CreatePurchase(ctx context.Context, p *models.PointsPurchase, key string) (*models.PointsPurchase, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `
		insert into points.points_purchases
			(seller_id, amount_cents, currency, cents_per_point, points, provider, idempotency_key)
		values ($1, $2, $3, $4, $5, $6, $7)
		returning id`,
		p.SellerID, p.AmountCents, p.Currency, p.CentsPerPoint, p.Points, p.Provider, key).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		existing, err := scanPurchase(r.db.QueryRow(ctx, purchaseSelect+`
			where pp.seller_id = $1 and pp.idempotency_key = $2`, p.SellerID, key))
		if err != nil {
			return nil, err
		}
		if existing.AmountCents != p.AmountCents || existing.Currency != p.Currency {
			return nil, ErrPurchaseKeyReused
		}
		return existing, nil
	}
	if err != nil {
		return nil, err
	}
	return r.GetPurchase(ctx, id)
}

// SetPurchaseCheckout stores where the provider sends the seller to pay.
func (r *PointsRepository) SetPurchaseCheckout(ctx context.Context, id uuid.UUID, url, reference *string) error {
	_, err := r.db.Exec(ctx, `
		update points.points_purchases
		set checkout_url = $2, provider_reference = coalesce($3, provider_reference), updated_at = now()
		where id = $1 and status = 'pending'`, id, url, reference)
	return err
}

// GetPurchase reads one purchase.
func (r *PointsRepository) GetPurchase(ctx context.Context, id uuid.UUID) (*models.PointsPurchase, error) {
	return scanPurchase(r.db.QueryRow(ctx, purchaseSelect+` where pp.id = $1`, id))
}

// GetSellerPurchase reads one of a seller's own purchases.
func (r *PointsRepository) GetSellerPurchase(ctx context.Context, sellerID, id uuid.UUID) (*models.PointsPurchase, error) {
	return scanPurchase(r.db.QueryRow(ctx, purchaseSelect+`
		where pp.id = $1 and pp.seller_id = $2`, id, sellerID))
}

// ListSellerPurchases is a seller's purchases, newest first.
func (r *PointsRepository) ListSellerPurchases(ctx context.Context, sellerID uuid.UUID, limit int) ([]models.PointsPurchase, error) {
	rows, err := r.db.Query(ctx, purchaseSelect+`
		where pp.seller_id = $1
		order by pp.created_at desc
		limit $2`, sellerID, limit)
	if err != nil {
		return nil, err
	}
	return scanPurchases(rows)
}

// ListPurchases is every seller's purchases for the admin, optionally one
// status only, newest first.
func (r *PointsRepository) ListPurchases(ctx context.Context, status string, limit int) ([]models.PointsPurchase, error) {
	rows, err := r.db.Query(ctx, purchaseSelect+`
		where ($1 = '' or pp.status = $1)
		order by pp.created_at desc
		limit $2`, status, limit)
	if err != nil {
		return nil, err
	}
	return scanPurchases(rows)
}

// PurchaseConfirmation is a payment confirmed for a purchase.
type PurchaseConfirmation struct {
	PaidAmountCents   int64
	ProviderReference *string
	// ConfirmedBy is "provider", "test" or "admin".
	ConfirmedBy string
	AdminID     *uuid.UUID
	// Audit is written with the credit when set.
	Audit *models.AuditEntry
}

// CompletePurchase credits the seller for a confirmed payment, in one
// transaction with the purchase row locked: the points are worked out from
// what was actually paid at the rate the purchase was quoted at, and a
// purchase already completed is never credited again. It reports whether
// this call was the one that credited it.
func (r *PointsRepository) CompletePurchase(ctx context.Context, id uuid.UUID, c PurchaseConfirmation) (*models.PointsPurchase, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	var (
		sellerID      uuid.UUID
		status        string
		centsPerPoint int64
	)
	err = tx.QueryRow(ctx, `
		select seller_id, status, cents_per_point
		from points.points_purchases where id = $1 for update`, id).
		Scan(&sellerID, &status, &centsPerPoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrPurchaseNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if status == models.PointsPurchaseCompleted {
		// A replayed confirmation: already credited, nothing more to do.
		_ = tx.Rollback(ctx)
		p, err := r.GetPurchase(ctx, id)
		return p, false, err
	}
	if status != models.PointsPurchasePending {
		return nil, false, ErrPurchaseState
	}
	points := c.PaidAmountCents / centsPerPoint
	if points <= 0 {
		return nil, false, ErrPurchaseAmount
	}
	actor := "system"
	if c.ConfirmedBy == "admin" {
		actor = "admin"
	}
	entryID, _, err := applyPoints(ctx, tx, PointsChange{
		SellerID: &sellerID, EntryType: models.PointsEntryPointsPurchase, Delta: points,
		ReferenceType: ref("points_purchase"), ReferenceID: &id,
		IdempotencyKey: "points-purchase:" + id.String(),
		ActorType:      actor,
		ActorID:        c.AdminID,
	})
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(ctx, `
		update points.points_purchases
		set status = 'completed', paid_amount_cents = $2, points_credited = $3,
		    provider_reference = coalesce($4, provider_reference), ledger_entry_id = $5,
		    confirmed_by = $6, confirmed_by_admin_id = $7, completed_at = now(), updated_at = now()
		where id = $1`, id, c.PaidAmountCents, points, c.ProviderReference, entryID,
		c.ConfirmedBy, c.AdminID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, false, ErrProviderReferenceUsed
		}
		return nil, false, err
	}
	if c.Audit != nil {
		c.Audit.After = map[string]any{
			"purchase_id": id, "seller_id": sellerID, "paid_amount_cents": c.PaidAmountCents,
			"points_credited": points,
		}
		if err := insertAudit(ctx, tx, *c.Audit); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	p, err := r.GetPurchase(ctx, id)
	return p, true, err
}

// FailPurchase marks a pending purchase failed. Nothing is credited.
func (r *PointsRepository) FailPurchase(ctx context.Context, id uuid.UUID, reason string, confirmedBy string, adminID *uuid.UUID, audit *models.AuditEntry) (*models.PointsPurchase, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		update points.points_purchases
		set status = 'failed', failure_reason = $2, confirmed_by = $3, confirmed_by_admin_id = $4,
		    failed_at = now(), updated_at = now()
		where id = $1 and status = 'pending'`, id, reason, confirmedBy, adminID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.GetPurchase(ctx, id); err != nil {
			return nil, err
		}
		return nil, ErrPurchaseState
	}
	if audit != nil {
		audit.After = map[string]any{"purchase_id": id, "status": "failed", "reason": reason}
		if err := insertAudit(ctx, tx, *audit); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.GetPurchase(ctx, id)
}

// CancelPurchase lets a seller abandon one of their pending purchases.
func (r *PointsRepository) CancelPurchase(ctx context.Context, sellerID, id uuid.UUID) (*models.PointsPurchase, error) {
	tag, err := r.db.Exec(ctx, `
		update points.points_purchases
		set status = 'cancelled', updated_at = now()
		where id = $1 and seller_id = $2 and status = 'pending'`, id, sellerID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.GetSellerPurchase(ctx, sellerID, id); err != nil {
			return nil, err
		}
		return nil, ErrPurchaseState
	}
	return r.GetSellerPurchase(ctx, sellerID, id)
}
