package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

// Advertising packages: an admin sets what a package costs, how many
// products it promotes and what each priority level earns a customer; a
// seller buys one and puts products in its levels.

var (
	ErrAdPackageNotFound  = errors.New("advertising package not found")
	ErrAdPackageState     = errors.New("that is not possible for a package in this state")
	ErrAdPurchaseNotFound = errors.New("advertising purchase not found")
	ErrAdPurchaseState    = errors.New("that is not possible for a purchase in this state")
	// ErrAdPaymentMismatch is a payment that is not for this purchase's
	// price and currency. Nothing is activated.
	ErrAdPaymentMismatch = errors.New("the payment does not match the purchase")
	ErrAdProductTaken    = errors.New("one of these products is already promoted by another package")
	ErrAdProductInvalid  = errors.New("a promoted product must be one of your published products")
	ErrAdLevelInvalid    = errors.New("that priority level is not part of this package")
)

// AdReminderThresholds are the budget marks a seller is told about, highest
// first.
var AdReminderThresholds = []int{100, 95, 90, 85, 80}

// productEntriesSQL is the game entries buying product p earns right now:
// its level's award while the seller's package is active, has that much
// budget left and the programme is on. Otherwise 0.
const productEntriesSQL = `coalesce((
	select pr.entries_per_purchase
	from ads.promotions pr
	inner join ads.purchases pu on pu.id = pr.purchase_id
	where pr.product_id = p.id and pr.removed_at is null
	  and pu.status = 'active' and pu.expires_at > now()
	  and pu.entries_pool - pu.entries_used - pu.entries_held >= pr.entries_per_purchase
	  and (select enabled and stopped_at is null from entries.program)), 0)`

// productSponsoredSQL is true while product p is on a seller's active
// promoted list, whether or not entries can be earned where the viewer is.
const productSponsoredSQL = `exists(
	select 1 from ads.promotions pr
	inner join ads.purchases pu on pu.id = pr.purchase_id
	where pr.product_id = p.id and pr.removed_at is null
	  and pu.status = 'active' and pu.expires_at > now())`

// productSponsoredRankSQL orders promoted products first, top level first.
// Products that are not promoted sort after them all.
const productSponsoredRankSQL = `coalesce((
	select pr.level from ads.promotions pr
	inner join ads.purchases pu on pu.id = pr.purchase_id
	where pr.product_id = p.id and pr.removed_at is null
	  and pu.status = 'active' and pu.expires_at > now()), 1000)`

type AdRepository struct {
	db *pgxpool.Pool
}

func NewAdRepository(db *pgxpool.Pool) *AdRepository { return &AdRepository{db: db} }

func (r *AdRepository) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ─── Packages ─────────────────────────────────────────────────────────────

const adPackageCols = `k.id, k.package_key, k.version, k.country_id, co.name, k.name, k.description,
	k.currency, k.price_minor, k.product_slots, k.level_entries, k.entries_pool, k.validity_days,
	k.status, k.published_at, k.retired_at, k.created_at, k.updated_at,
	(select count(*) from ads.purchases pu where pu.package_id = k.id and pu.paid_at is not null)`

const adPackageFrom = ` from ads.packages k inner join core.countries co on co.id = k.country_id`

func scanAdPackage(row scanner) (*models.AdPackage, error) {
	p := &models.AdPackage{}
	err := row.Scan(&p.ID, &p.PackageKey, &p.Version, &p.CountryID, &p.CountryName, &p.Name, &p.Description,
		&p.Currency, &p.PriceMinor, &p.ProductSlots, &p.LevelEntries, &p.EntriesPool, &p.ValidityDays,
		&p.Status, &p.PublishedAt, &p.RetiredAt, &p.CreatedAt, &p.UpdatedAt, &p.Purchases)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAdPackageNotFound
	}
	return p, err
}

func scanAdPackages(rows pgx.Rows) ([]models.AdPackage, error) {
	defer rows.Close()
	out := []models.AdPackage{}
	for rows.Next() {
		p, err := scanAdPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func getAdPackage(ctx context.Context, q querier, id uuid.UUID, lock bool) (*models.AdPackage, error) {
	sql := `select ` + adPackageCols + adPackageFrom + ` where k.id = $1`
	if lock {
		sql += ` for update of k`
	}
	return scanAdPackage(q.QueryRow(ctx, sql, id))
}

func (r *AdRepository) GetPackage(ctx context.Context, id uuid.UUID) (*models.AdPackage, error) {
	return getAdPackage(ctx, r.db, id, false)
}

// ListPackages is every package version, newest first within a package.
func (r *AdRepository) ListPackages(ctx context.Context) ([]models.AdPackage, error) {
	rows, err := r.db.Query(ctx, `select `+adPackageCols+adPackageFrom+`
		order by co.name, k.price_minor, k.package_key, k.version desc`)
	if err != nil {
		return nil, err
	}
	return scanAdPackages(rows)
}

// PublishedPackages are the packages a seller in a country can buy.
func (r *AdRepository) PublishedPackages(ctx context.Context, countryID uuid.UUID) ([]models.AdPackage, error) {
	rows, err := r.db.Query(ctx, `select `+adPackageCols+adPackageFrom+`
		where k.country_id = $1 and k.status = 'published'
		order by k.price_minor`, countryID)
	if err != nil {
		return nil, err
	}
	return scanAdPackages(rows)
}

// SavePackageDraft creates a draft (id nil) or rewrites one. A package that
// has been published cannot be rewritten: NewPackageVersion copies it.
func (r *AdRepository) SavePackageDraft(ctx context.Context, id *uuid.UUID, p *models.AdPackage, adminID uuid.UUID, audit models.AuditEntry) (*models.AdPackage, error) {
	var out *models.AdPackage
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var saved uuid.UUID
		if id == nil {
			if err := tx.QueryRow(ctx, `
				insert into ads.packages (country_id, name, description, currency, price_minor,
					product_slots, level_entries, entries_pool, validity_days,
					created_by_admin_id, updated_by_admin_id)
				values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)
				returning id`,
				p.CountryID, p.Name, p.Description, p.Currency, p.PriceMinor,
				p.ProductSlots, p.LevelEntries, p.EntriesPool, p.ValidityDays, adminID).Scan(&saved); err != nil {
				return err
			}
		} else {
			cur, err := getAdPackage(ctx, tx, *id, true)
			if err != nil {
				return err
			}
			if cur.Status != models.AdPackageDraft {
				return ErrAdPackageState
			}
			if _, err := tx.Exec(ctx, `
				update ads.packages
				set country_id = $2, name = $3, description = $4, currency = $5, price_minor = $6,
				    product_slots = $7, level_entries = $8, entries_pool = $9, validity_days = $10,
				    updated_by_admin_id = $11, updated_at = now()
				where id = $1`,
				*id, p.CountryID, p.Name, p.Description, p.Currency, p.PriceMinor,
				p.ProductSlots, p.LevelEntries, p.EntriesPool, p.ValidityDays, adminID); err != nil {
				return err
			}
			saved = *id
		}
		audit.EntityID = &saved
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		var err error
		out, err = getAdPackage(ctx, tx, saved, false)
		return err
	})
	return out, err
}

// NewPackageVersion copies a published or retired package into a new draft
// version, so it can be changed without touching what sellers bought.
func (r *AdRepository) NewPackageVersion(ctx context.Context, id, adminID uuid.UUID, audit models.AuditEntry) (*models.AdPackage, error) {
	var out *models.AdPackage
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := getAdPackage(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Status == models.AdPackageDraft {
			return ErrAdPackageState
		}
		var saved uuid.UUID
		if err := tx.QueryRow(ctx, `
			insert into ads.packages (package_key, version, country_id, name, description, currency,
				price_minor, product_slots, level_entries, entries_pool, validity_days,
				created_by_admin_id, updated_by_admin_id)
			select package_key,
			       (select max(version) + 1 from ads.packages where package_key = k.package_key),
			       country_id, name, description, currency, price_minor, product_slots, level_entries,
			       entries_pool, validity_days, $2, $2
			from ads.packages k where k.id = $1
			returning id`, id, adminID).Scan(&saved); err != nil {
			return err
		}
		audit.EntityID = &saved
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out, err = getAdPackage(ctx, tx, saved, false)
		return err
	})
	return out, err
}

// PublishPackage puts a draft on sale. The version of the same package that
// was on sale is retired in the same step; purchases of it keep their terms.
func (r *AdRepository) PublishPackage(ctx context.Context, id, adminID uuid.UUID, audit models.AuditEntry) (*models.AdPackage, error) {
	var out *models.AdPackage
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := getAdPackage(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Status != models.AdPackageDraft {
			return ErrAdPackageState
		}
		if _, err := tx.Exec(ctx, `
			update ads.packages
			set status = 'retired', retired_at = now(), updated_by_admin_id = $2, updated_at = now()
			where package_key = $1 and status = 'published'`, cur.PackageKey, adminID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update ads.packages
			set status = 'published', published_at = now(), updated_by_admin_id = $2, updated_at = now()
			where id = $1`, id, adminID); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out, err = getAdPackage(ctx, tx, id, false)
		return err
	})
	return out, err
}

// RetirePackage takes a published package off sale.
func (r *AdRepository) RetirePackage(ctx context.Context, id, adminID uuid.UUID, audit models.AuditEntry) (*models.AdPackage, error) {
	var out *models.AdPackage
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := getAdPackage(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Status != models.AdPackagePublished {
			return ErrAdPackageState
		}
		if _, err := tx.Exec(ctx, `
			update ads.packages
			set status = 'retired', retired_at = now(), updated_by_admin_id = $2, updated_at = now()
			where id = $1`, id, adminID); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, audit); err != nil {
			return err
		}
		out, err = getAdPackage(ctx, tx, id, false)
		return err
	})
	return out, err
}

// DeletePackageDraft removes a draft nobody could have bought.
func (r *AdRepository) DeletePackageDraft(ctx context.Context, id uuid.UUID, audit models.AuditEntry) error {
	return r.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := getAdPackage(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Status != models.AdPackageDraft {
			return ErrAdPackageState
		}
		if _, err := tx.Exec(ctx, `delete from ads.packages where id = $1`, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// ─── Purchases ────────────────────────────────────────────────────────────

const adPurchaseCols = `pu.id, pu.seller_id, coalesce(se.trading_name, se.legal_name, ''), se.email::text,
	pu.package_id, pu.package_name, pu.country_id, pu.currency, pu.price_minor, pu.product_slots,
	pu.level_entries, pu.entries_pool, pu.validity_days, pu.status, pu.entries_used, pu.entries_held,
	pu.provider, pu.provider_reference, pu.checkout_url, pu.receipt_url, pu.paid_amount_minor,
	pu.failure_reason, pu.last_reminder_threshold, pu.paid_at, pu.expires_at, pu.ended_at, pu.created_at`

const adPurchaseFrom = ` from ads.purchases pu inner join seller.sellers se on se.id = pu.seller_id`

func usagePercent(p *models.AdPurchase) int {
	if p.Status == models.AdPurchaseExhausted {
		return 100
	}
	if p.EntriesPool <= 0 {
		return 0
	}
	return p.EntriesUsed * 100 / p.EntriesPool
}

func scanAdPurchase(row scanner) (*models.AdPurchase, error) {
	p := &models.AdPurchase{}
	err := row.Scan(&p.ID, &p.SellerID, &p.SellerName, &p.SellerEmail,
		&p.PackageID, &p.PackageName, &p.CountryID, &p.Currency, &p.PriceMinor, &p.ProductSlots,
		&p.LevelEntries, &p.EntriesPool, &p.ValidityDays, &p.Status, &p.EntriesUsed, &p.EntriesHeld,
		&p.Provider, &p.ProviderReference, &p.CheckoutURL, &p.ReceiptURL, &p.PaidAmountMinor,
		&p.FailureReason, &p.LastReminder, &p.PaidAt, &p.ExpiresAt, &p.EndedAt, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAdPurchaseNotFound
	}
	if err != nil {
		return nil, err
	}
	p.UsagePercent = usagePercent(p)
	return p, nil
}

func scanAdPurchases(rows pgx.Rows) ([]models.AdPurchase, error) {
	defer rows.Close()
	out := []models.AdPurchase{}
	for rows.Next() {
		p, err := scanAdPurchase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func getAdPurchase(ctx context.Context, q querier, id uuid.UUID, lock bool) (*models.AdPurchase, error) {
	sql := `select ` + adPurchaseCols + adPurchaseFrom + ` where pu.id = $1`
	if lock {
		sql += ` for update of pu`
	}
	return scanAdPurchase(q.QueryRow(ctx, sql, id))
}

func (r *AdRepository) GetPurchase(ctx context.Context, id uuid.UUID) (*models.AdPurchase, error) {
	return getAdPurchase(ctx, r.db, id, false)
}

// GetSellerPurchase is a purchase only if it is this seller's.
func (r *AdRepository) GetSellerPurchase(ctx context.Context, sellerID, id uuid.UUID) (*models.AdPurchase, error) {
	p, err := getAdPurchase(ctx, r.db, id, false)
	if err != nil {
		return nil, err
	}
	if p.SellerID != sellerID {
		return nil, ErrAdPurchaseNotFound
	}
	return p, nil
}

// SellerCountry is the country a seller sells from.
func (r *AdRepository) SellerCountry(ctx context.Context, sellerID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `select country_id from seller.sellers where id = $1`, sellerID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrAdPurchaseNotFound
	}
	return id, err
}

// CreatePurchase starts a seller's purchase of a published package, pending
// until the payment is confirmed. The same key returns the same purchase.
func (r *AdRepository) CreatePurchase(ctx context.Context, sellerID uuid.UUID, pkg *models.AdPackage, provider, key string) (*models.AdPurchase, error) {
	var out *models.AdPurchase
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			insert into ads.purchases (seller_id, package_id, package_name, country_id, currency, price_minor,
				product_slots, level_entries, entries_pool, validity_days, provider, idempotency_key)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			on conflict (seller_id, idempotency_key) do nothing
			returning id`,
			sellerID, pkg.ID, pkg.Name, pkg.CountryID, pkg.Currency, pkg.PriceMinor,
			pkg.ProductSlots, pkg.LevelEntries, pkg.EntriesPool, pkg.ValidityDays, provider, key).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `
				select id from ads.purchases where seller_id = $1 and idempotency_key = $2`, sellerID, key).Scan(&id)
		}
		if err != nil {
			return err
		}
		out, err = getAdPurchase(ctx, tx, id, false)
		return err
	})
	return out, err
}

// SetPurchaseCheckout records where the seller pays and the provider's
// reference for it.
func (r *AdRepository) SetPurchaseCheckout(ctx context.Context, id uuid.UUID, url, reference *string) error {
	_, err := r.db.Exec(ctx, `
		update ads.purchases
		set checkout_url = $2, provider_reference = coalesce($3, provider_reference), updated_at = now()
		where id = $1 and status = 'pending'`, id, url, reference)
	return err
}

// AdPayment is a confirmed payment for a purchase.
type AdPayment struct {
	AmountMinor int64
	Currency    string
	Reference   *string
	ReceiptURL  *string
}

// CompletePurchase activates a purchase whose payment is confirmed: its
// budget and time start now. The amount and currency must be the
// purchase's own, or nothing is activated. A purchase already settled is
// returned untouched, so a confirmation sent twice counts once.
func (r *AdRepository) CompletePurchase(ctx context.Context, id uuid.UUID, pay AdPayment) (*models.AdPurchase, bool, error) {
	var (
		out       *models.AdPurchase
		activated bool
	)
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := getAdPurchase(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Status != models.AdPurchasePending {
			out = cur
			return nil
		}
		if pay.AmountMinor != cur.PriceMinor || !sameCurrency(pay.Currency, cur.Currency) {
			return fmt.Errorf("%w: paid %d %s, the package costs %d %s", ErrAdPaymentMismatch,
				pay.AmountMinor, pay.Currency, cur.PriceMinor, cur.Currency)
		}
		if _, err := tx.Exec(ctx, `
			update ads.purchases
			set status = 'active', paid_at = now(), paid_amount_minor = $2,
			    expires_at = now() + make_interval(days => validity_days),
			    provider_reference = coalesce($3, provider_reference),
			    receipt_url = coalesce($4, receipt_url), checkout_url = null, updated_at = now()
			where id = $1`, id, pay.AmountMinor, pay.Reference, pay.ReceiptURL); err != nil {
			return mapAdWriteErr(err)
		}
		activated = true
		out, err = getAdPurchase(ctx, tx, id, false)
		return err
	})
	return out, activated, err
}

func sameCurrency(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if x >= 'a' && x <= 'z' {
			x -= 32
		}
		if y >= 'a' && y <= 'z' {
			y -= 32
		}
		if x != y {
			return false
		}
	}
	return true
}

func mapAdWriteErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		if pg.ConstraintName == "promotions_product_uq" {
			return ErrAdProductTaken
		}
		return ErrAdPurchaseState
	}
	return err
}

// EndPurchase closes a pending purchase as failed or cancelled.
func (r *AdRepository) EndPurchase(ctx context.Context, id uuid.UUID, status, reason string) (*models.AdPurchase, error) {
	tag, err := r.db.Exec(ctx, `
		update ads.purchases
		set status = $2, failure_reason = $3, ended_at = now(), checkout_url = null, updated_at = now()
		where id = $1 and status = 'pending'`, id, status, reason)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if _, err := getAdPurchase(ctx, r.db, id, false); err != nil {
			return nil, err
		}
		return nil, ErrAdPurchaseState
	}
	return getAdPurchase(ctx, r.db, id, false)
}

// PendingPurchases are purchases still waiting on a provider's confirmation.
func (r *AdRepository) PendingPurchases(ctx context.Context, provider string, limit int) ([]models.AdPurchase, error) {
	rows, err := r.db.Query(ctx, `select `+adPurchaseCols+adPurchaseFrom+`
		where pu.status = 'pending' and pu.provider = $1 and pu.provider_reference is not null
		order by pu.created_at
		limit $2`, provider, limit)
	if err != nil {
		return nil, err
	}
	return scanAdPurchases(rows)
}

func (r *AdRepository) promotions(ctx context.Context, q querier, purchaseIDs []uuid.UUID) (map[uuid.UUID][]models.AdPromotion, error) {
	// A product keeps one line per purchase: what it earned across every
	// level it has sat in, with the level it holds now (if it is still on
	// the list).
	rows, err := q.Query(ctx, `
		select max(pr.id::text) filter (where pr.removed_at is null),
		       pr.purchase_id, pr.product_id, p.name, p.image_url,
		       coalesce(max(pr.level) filter (where pr.removed_at is null), 0),
		       coalesce(max(pr.entries_per_purchase) filter (where pr.removed_at is null), 0),
		       sum(pr.entries_granted)::int, sum(pr.orders_count)::int,
		       case when bool_or(pr.removed_at is null) then null else max(pr.removed_at) end
		from ads.promotions pr
		inner join seller.products p on p.id = pr.product_id
		where pr.purchase_id = any($1)
		group by pr.purchase_id, pr.product_id, p.name, p.image_url
		order by 6 = 0, 6, p.name`, purchaseIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID][]models.AdPromotion{}
	for rows.Next() {
		var (
			pr models.AdPromotion
			id *string
		)
		if err := rows.Scan(&id, &pr.PurchaseID, &pr.ProductID, &pr.ProductName, &pr.ProductImageURL,
			&pr.Level, &pr.EntriesPerPurchase, &pr.EntriesGranted, &pr.OrdersCount, &pr.RemovedAt); err != nil {
			return nil, err
		}
		if id != nil {
			pr.ID, _ = uuid.Parse(*id)
		}
		out[pr.PurchaseID] = append(out[pr.PurchaseID], pr)
	}
	return out, rows.Err()
}

func (r *AdRepository) withPromotions(ctx context.Context, list []models.AdPurchase) ([]models.AdPurchase, error) {
	if len(list) == 0 {
		return list, nil
	}
	ids := make([]uuid.UUID, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	byPurchase, err := r.promotions(ctx, r.db, ids)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Promotions = byPurchase[list[i].ID]
		if list[i].Promotions == nil {
			list[i].Promotions = []models.AdPromotion{}
		}
	}
	return list, nil
}

// SellerPurchases is a seller's purchases with their promoted products.
func (r *AdRepository) SellerPurchases(ctx context.Context, sellerID uuid.UUID, limit int) ([]models.AdPurchase, error) {
	rows, err := r.db.Query(ctx, `select `+adPurchaseCols+adPurchaseFrom+`
		where pu.seller_id = $1
		order by (pu.status = 'active') desc, pu.created_at desc
		limit $2`, sellerID, limit)
	if err != nil {
		return nil, err
	}
	list, err := scanAdPurchases(rows)
	if err != nil {
		return nil, err
	}
	return r.withPromotions(ctx, list)
}

// AdminPurchases is every seller's purchases, optionally one status.
func (r *AdRepository) AdminPurchases(ctx context.Context, status string, limit int) ([]models.AdPurchase, error) {
	rows, err := r.db.Query(ctx, `select `+adPurchaseCols+adPurchaseFrom+`
		where ($1 = '' or pu.status = $1)
		order by pu.created_at desc
		limit $2`, status, limit)
	if err != nil {
		return nil, err
	}
	list, err := scanAdPurchases(rows)
	if err != nil {
		return nil, err
	}
	return r.withPromotions(ctx, list)
}

// PromotionSlot is a product a seller wants in a level.
type PromotionSlot struct {
	ProductID uuid.UUID
	Level     int
}

// SetPromotions makes the seller's promoted list for a purchase exactly
// these products in these levels. A product that keeps its level is left
// alone; anything else is taken off and put on afresh, so what a product
// earned in a level stays on record. Entries already promised to unpaid
// orders are not affected: they belong to the order.
func (r *AdRepository) SetPromotions(ctx context.Context, sellerID, purchaseID uuid.UUID, slots []PromotionSlot) (*models.AdPurchase, error) {
	var out *models.AdPurchase
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		pu, err := getAdPurchase(ctx, tx, purchaseID, true)
		if err != nil {
			return err
		}
		if pu.SellerID != sellerID {
			return ErrAdPurchaseNotFound
		}
		var live bool
		if err := tx.QueryRow(ctx, `
			select status = 'active' and expires_at > now() from ads.purchases where id = $1`, purchaseID).
			Scan(&live); err != nil {
			return err
		}
		if !live {
			return ErrAdPurchaseState
		}
		seenLevel, seenProduct := map[int]bool{}, map[uuid.UUID]bool{}
		for _, s := range slots {
			if s.Level < 1 || s.Level > pu.ProductSlots || seenLevel[s.Level] || seenProduct[s.ProductID] {
				return ErrAdLevelInvalid
			}
			seenLevel[s.Level], seenProduct[s.ProductID] = true, true
			var ok bool
			if err := tx.QueryRow(ctx, `
				select exists(
					select 1 from seller.products p
					inner join seller.shops s on s.id = p.shop_id
					where p.id = $1 and s.seller_id = $2 and p.status = 'published')`, s.ProductID, sellerID).
				Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return ErrAdProductInvalid
			}
		}
		// Take off whatever is no longer wanted where it is.
		rows, err := tx.Query(ctx, `
			select id, product_id, level from ads.promotions
			where purchase_id = $1 and removed_at is null for update`, purchaseID)
		if err != nil {
			return err
		}
		kept := map[uuid.UUID]bool{}
		var drop []uuid.UUID
		for rows.Next() {
			var (
				id, product uuid.UUID
				level       int
			)
			if err := rows.Scan(&id, &product, &level); err != nil {
				rows.Close()
				return err
			}
			keep := false
			for _, s := range slots {
				if s.ProductID == product && s.Level == level {
					keep = true
				}
			}
			if keep {
				kept[product] = true
			} else {
				drop = append(drop, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(drop) > 0 {
			if _, err := tx.Exec(ctx, `
				update ads.promotions set removed_at = now(), updated_at = now() where id = any($1)`, drop); err != nil {
				return err
			}
		}
		for _, s := range slots {
			if kept[s.ProductID] {
				continue
			}
			if _, err := tx.Exec(ctx, `
				insert into ads.promotions (purchase_id, seller_id, product_id, level, entries_per_purchase)
				values ($1, $2, $3, $4, $5)`,
				purchaseID, sellerID, s.ProductID, s.Level, pu.LevelEntries[s.Level-1]); err != nil {
				return mapAdWriteErr(err)
			}
		}
		out, err = getAdPurchase(ctx, tx, purchaseID, false)
		if err != nil {
			return err
		}
		byPurchase, err := r.promotions(ctx, tx, []uuid.UUID{purchaseID})
		if err != nil {
			return err
		}
		out.Promotions = byPurchase[purchaseID]
		if out.Promotions == nil {
			out.Promotions = []models.AdPromotion{}
		}
		return nil
	})
	return out, err
}

// ─── Budget use and reminders ─────────────────────────────────────────────

// afterPackageUse runs whenever a purchase's budget moves. It ends the
// package once no level can be awarded any more, and records the highest
// budget reminder not yet sent. A package that passes several marks at
// once gets only the top one; a mark is never sent twice, even if a refund
// later takes the usage back below it.
func afterPackageUse(ctx context.Context, q querier, purchaseID uuid.UUID) error {
	var (
		sellerID                uuid.UUID
		status                  string
		pool, used, held, last  int
		smallest                int
		stillRunning, exhausted bool
	)
	if err := q.QueryRow(ctx, `
		select seller_id, status, entries_pool, entries_used, entries_held, last_reminder_threshold,
		       (select min(e) from unnest(level_entries) e), expires_at > now()
		from ads.purchases where id = $1 for update`, purchaseID).
		Scan(&sellerID, &status, &pool, &used, &held, &last, &smallest, &stillRunning); err != nil {
		return err
	}
	if status != models.AdPurchaseActive && status != models.AdPurchaseExhausted {
		return nil
	}
	percent := used * 100 / pool
	// Used up: what is left cannot pay even the smallest level, and nothing
	// is on hold that might come back.
	if status == models.AdPurchaseActive && held == 0 && pool-used < smallest {
		exhausted = true
		if _, err := q.Exec(ctx, `
			update ads.purchases set status = 'exhausted', ended_at = now(), updated_at = now()
			where id = $1`, purchaseID); err != nil {
			return err
		}
	}
	if exhausted || status == models.AdPurchaseExhausted {
		percent = 100
	}
	threshold := 0
	for _, t := range AdReminderThresholds {
		if percent >= t {
			threshold = t
			break
		}
	}
	if threshold == 0 || threshold <= last {
		return nil
	}
	if _, err := q.Exec(ctx, `
		insert into ads.reminders (purchase_id, seller_id, threshold, usage_percent)
		values ($1, $2, $3, $4)
		on conflict (purchase_id, threshold) where kind = 'auto' do nothing`,
		purchaseID, sellerID, threshold, percent); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `
		update ads.purchases set last_reminder_threshold = $2, updated_at = now() where id = $1`,
		purchaseID, threshold)
	return err
}

// ReminderMessage is the wording of a budget notice.
func ReminderMessage(packageName string, threshold int) string {
	if threshold >= 100 {
		return fmt.Sprintf("Your %s advertising budget is fully used. Your promoted products have left the promoted list. Buy a new package to promote them again.", packageName)
	}
	return fmt.Sprintf("%d%% of your %s advertising budget has been used. Buy another package to keep your products promoted when it runs out.", threshold, packageName)
}

func scanAdReminder(row scanner, rem *models.AdReminder) error {
	if err := row.Scan(&rem.ID, &rem.PurchaseID, &rem.PackageName, &rem.Threshold, &rem.UsagePercent,
		&rem.Kind, &rem.ReadAt, &rem.CreatedAt); err != nil {
		return err
	}
	rem.Message = ReminderMessage(rem.PackageName, rem.Threshold)
	return nil
}

// SellerReminders are a seller's budget notices, newest first.
func (r *AdRepository) SellerReminders(ctx context.Context, sellerID uuid.UUID, limit int) ([]models.AdReminder, error) {
	rows, err := r.db.Query(ctx, `
		select rm.id, rm.purchase_id, pu.package_name, rm.threshold, rm.usage_percent, rm.kind,
		       rm.read_at, rm.created_at
		from ads.reminders rm
		inner join ads.purchases pu on pu.id = rm.purchase_id
		where rm.seller_id = $1
		order by rm.created_at desc
		limit $2`, sellerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.AdReminder{}
	for rows.Next() {
		var rem models.AdReminder
		if err := scanAdReminder(rows, &rem); err != nil {
			return nil, err
		}
		out = append(out, rem)
	}
	return out, rows.Err()
}

// MarkRemindersRead marks a seller's notices as seen.
func (r *AdRepository) MarkRemindersRead(ctx context.Context, sellerID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update ads.reminders set read_at = now() where seller_id = $1 and read_at is null`, sellerID)
	return err
}

// UnsentReminders are notices not yet emailed.
func (r *AdRepository) UnsentReminders(ctx context.Context, limit int) ([]models.AdReminderEmail, error) {
	rows, err := r.db.Query(ctx, `
		select rm.id, rm.purchase_id, pu.package_name, rm.threshold, rm.usage_percent, rm.kind,
		       rm.read_at, rm.created_at, se.email::text, coalesce(se.trading_name, se.legal_name, '')
		from ads.reminders rm
		inner join ads.purchases pu on pu.id = rm.purchase_id
		inner join seller.sellers se on se.id = rm.seller_id
		where rm.emailed_at is null
		order by rm.created_at
		limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AdReminderEmail
	for rows.Next() {
		var e models.AdReminderEmail
		if err := rows.Scan(&e.ID, &e.PurchaseID, &e.PackageName, &e.Threshold, &e.UsagePercent, &e.Kind,
			&e.ReadAt, &e.CreatedAt, &e.SellerEmail, &e.SellerName); err != nil {
			return nil, err
		}
		e.Message = ReminderMessage(e.PackageName, e.Threshold)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkReminderEmailed records that a notice's email is queued.
func (r *AdRepository) MarkReminderEmailed(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `update ads.reminders set emailed_at = now() where id = $1`, id)
	return err
}

// ResendReminder sends a seller the notice for where their budget stands
// now, at an admin's request. It is refused below the first mark.
func (r *AdRepository) ResendReminder(ctx context.Context, purchaseID, adminID uuid.UUID, audit models.AuditEntry) (*models.AdReminder, error) {
	rem := &models.AdReminder{}
	err := r.inTx(ctx, func(tx pgx.Tx) error {
		pu, err := getAdPurchase(ctx, tx, purchaseID, true)
		if err != nil {
			return err
		}
		threshold := 0
		for _, t := range AdReminderThresholds {
			if pu.UsagePercent >= t {
				threshold = t
				break
			}
		}
		if threshold == 0 {
			return ErrAdPurchaseState
		}
		if err := scanAdReminder(tx.QueryRow(ctx, `
			insert into ads.reminders (purchase_id, seller_id, threshold, usage_percent, kind, sent_by_admin_id)
			values ($1, $2, $3, $4, 'resend', $5)
			returning id, purchase_id, $6::text, threshold, usage_percent, kind, read_at, created_at`,
			purchaseID, pu.SellerID, threshold, pu.UsagePercent, adminID, pu.PackageName), rem); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
	return rem, err
}
