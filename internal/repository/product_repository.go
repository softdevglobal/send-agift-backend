package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrProductNotFound  = errors.New("product not found")
	ErrProductDuplicate = errors.New("product already exists")
	ErrInventoryNotFound = errors.New("inventory not found")
)

type ProductRepository struct {
	db *pgxpool.Pool
}

func NewProductRepository(db *pgxpool.Pool) *ProductRepository {
	return &ProductRepository{db: db}
}

const productSelectCols = `
	p.id, p.shop_id, p.name, p.slug, p.description, p.product_type, p.price_amount,
	p.currency, p.status, p.occasion_tags, p.customer_type_visibility,
	p.points_display_enabled, p.prep_minutes, p.created_at, p.updated_at, p.image_url,
	p.parcel_length, p.parcel_width, p.parcel_height, p.parcel_distance_unit,
	p.parcel_weight, p.parcel_mass_unit`

func scanProduct(scanner interface {
	Scan(dest ...any) error
}, p *models.Product) error {
	var length, width, height, distanceUnit, weight, massUnit *string
	if err := scanner.Scan(
		&p.ID, &p.ShopID, &p.Name, &p.Slug, &p.Description, &p.ProductType, &p.PriceAmount,
		&p.Currency, &p.Status, &p.OccasionTags, &p.CustomerTypeVisibility,
		&p.PointsDisplayEnabled, &p.PrepMinutes, &p.CreatedAt, &p.UpdatedAt, &p.ImageURL,
		&length, &width, &height, &distanceUnit, &weight, &massUnit,
	); err != nil {
		return err
	}
	p.Parcel = models.ProductParcelFromNullable(length, width, height, distanceUnit, weight, massUnit)
	return nil
}

func parcelArgs(p *models.Product) (length, width, height, distanceUnit, weight, massUnit any) {
	if p.Parcel == nil {
		return nil, nil, nil, nil, nil, nil
	}
	return nullIfBlank(p.Parcel.Length), nullIfBlank(p.Parcel.Width), nullIfBlank(p.Parcel.Height),
		nullIfBlank(p.Parcel.DistanceUnit), nullIfBlank(p.Parcel.Weight), nullIfBlank(p.Parcel.MassUnit)
}

func nullIfBlank(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

func (r *ProductRepository) Create(ctx context.Context, p *models.Product, assets []models.MediaAsset) error {
	if p.OccasionTags == nil {
		p.OccasionTags = []string{}
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	pl, pw, ph, pdu, pwt, pmu := parcelArgs(p)
	err = tx.QueryRow(ctx, `
		insert into seller.products (
			shop_id, name, slug, description, product_type, price_amount, currency,
			status, occasion_tags, customer_type_visibility, points_display_enabled, prep_minutes, image_url,
			parcel_length, parcel_width, parcel_height, parcel_distance_unit, parcel_weight, parcel_mass_unit
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		returning id, status, created_at, updated_at`,
		p.ShopID, p.Name, p.Slug, p.Description, p.ProductType, p.PriceAmount, p.Currency,
		p.Status, p.OccasionTags, p.CustomerTypeVisibility, p.PointsDisplayEnabled, p.PrepMinutes, p.ImageURL,
		pl, pw, ph, pdu, pwt, pmu,
	).Scan(&p.ID, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return mapProductWriteError(err)
	}
	if err := insertProductMediaTx(ctx, tx, p.ID, assets); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *ProductRepository) Update(ctx context.Context, p *models.Product, assets []models.MediaAsset, replaceMedia bool) error {
	if p.OccasionTags == nil {
		p.OccasionTags = []string{}
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if replaceMedia {
		var oldAssetIDs []uuid.UUID
		rows, err := tx.Query(ctx,
			`select media_asset_id from seller.product_media where product_id = $1`, p.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			oldAssetIDs = append(oldAssetIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(oldAssetIDs) > 0 {
			// Cascades to seller.product_media.
			if _, err := tx.Exec(ctx,
				`delete from media.media_assets where id = any($1)`, oldAssetIDs); err != nil {
				return err
			}
		}
		if err := insertProductMediaTx(ctx, tx, p.ID, assets); err != nil {
			return err
		}
	}

	pl, pw, ph, pdu, pwt, pmu := parcelArgs(p)
	err = tx.QueryRow(ctx, `
		update seller.products
		set name = $2,
		    slug = $3,
		    description = $4,
		    product_type = $5,
		    price_amount = $6,
		    currency = $7,
		    status = $8,
		    occasion_tags = $9,
		    customer_type_visibility = $10,
		    points_display_enabled = $11,
		    prep_minutes = $12,
		    image_url = $13,
		    parcel_length = $14,
		    parcel_width = $15,
		    parcel_height = $16,
		    parcel_distance_unit = $17,
		    parcel_weight = $18,
		    parcel_mass_unit = $19,
		    updated_at = now()
		where id = $1
		returning updated_at`,
		p.ID, p.Name, p.Slug, p.Description, p.ProductType, p.PriceAmount, p.Currency,
		p.Status, p.OccasionTags, p.CustomerTypeVisibility, p.PointsDisplayEnabled, p.PrepMinutes, p.ImageURL,
		pl, pw, ph, pdu, pwt, pmu,
	).Scan(&p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProductNotFound
	}
	if err != nil {
		return mapProductWriteError(err)
	}
	return tx.Commit(ctx)
}

func (r *ProductRepository) Delete(ctx context.Context, productID string) error {
	tag, err := r.db.Exec(ctx, `delete from seller.products where id = $1`, productID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrProductNotFound
	}
	return nil
}

// ExistsByID returns true when a product row exists (used by customer saved gifts).
func (r *ProductRepository) ExistsByID(ctx context.Context, productID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		select exists(select 1 from seller.products where id = $1)`, productID,
	).Scan(&exists)
	return exists, err
}

func (r *ProductRepository) GetByIDForSeller(ctx context.Context, sellerID, productID string) (*models.Product, error) {
	p := &models.Product{}
	err := scanProduct(r.db.QueryRow(ctx, `
		select `+productSelectCols+`
		from seller.products p
		inner join seller.shops s on s.id = p.shop_id
		where p.id = $1 and s.seller_id = $2`, productID, sellerID), p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.OccasionTags == nil {
		p.OccasionTags = []string{}
	}
	if err := r.attachMedia(ctx, []*models.Product{p}); err != nil {
		return nil, err
	}
	return p, nil
}

func (r *ProductRepository) ListByShopForSeller(ctx context.Context, sellerID, shopID string) ([]models.Product, error) {
	rows, err := r.db.Query(ctx, `
		select `+productSelectCols+`
		from seller.products p
		inner join seller.shops s on s.id = p.shop_id
		where p.shop_id = $1 and s.seller_id = $2
		order by p.created_at desc`, shopID, sellerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []models.Product{}
	for rows.Next() {
		var p models.Product
		if err := scanProduct(rows, &p); err != nil {
			return nil, err
		}
		if p.OccasionTags == nil {
			p.OccasionTags = []string{}
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ptrs := make([]*models.Product, len(items))
	for i := range items {
		ptrs[i] = &items[i]
	}
	if err := r.attachMedia(ctx, ptrs); err != nil {
		return nil, err
	}
	return items, nil
}

// ListPublishedByShopForCustomerType returns only published products for a given shop,
// where the shop is active and the product is visible for the given customer_type.
// customerType must be 'personal' or 'corporate'.
func (r *ProductRepository) ListPublishedByShopForCustomerType(
	ctx context.Context,
	shopID string,
	customerType string,
) ([]models.Product, error) {
	rows, err := r.db.Query(ctx, `
		select `+productSelectCols+`
		from seller.products p
		inner join seller.shops s on s.id = p.shop_id
		where p.shop_id = $1
		  and s.status = 'active'
		  and p.status = 'published'
		  and (p.customer_type_visibility = 'both' or p.customer_type_visibility = $2)
		order by p.created_at desc`, shopID, customerType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []models.Product{}
	for rows.Next() {
		var p models.Product
		if err := scanProduct(rows, &p); err != nil {
			return nil, err
		}
		if p.OccasionTags == nil {
			p.OccasionTags = []string{}
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ptrs := make([]*models.Product, len(items))
	for i := range items {
		ptrs[i] = &items[i]
	}
	if err := r.attachMedia(ctx, ptrs); err != nil {
		return nil, err
	}
	return items, nil
}

// GetPublishedByIDForCustomerType returns one published product from an active shop,
// for public product pages. Applies the same visibility rules as the shop listing,
// and joins the shop so the page can render "sold by" without a second call.
// customerType must be 'personal' or 'corporate'.
func (r *ProductRepository) GetPublishedByIDForCustomerType(
	ctx context.Context,
	productID string,
	customerType string,
) (*models.PublicProduct, error) {
	out := &models.PublicProduct{}
	var length, width, height, distanceUnit, weight, massUnit *string
	err := r.db.QueryRow(ctx, `
		select `+productSelectCols+`,
		       s.id, s.name, s.slug, s.image_url, s.customer_visible_location
		from seller.products p
		inner join seller.shops s on s.id = p.shop_id
		where p.id = $1
		  and s.status = 'active'
		  and p.status = 'published'
		  and (p.customer_type_visibility = 'both' or p.customer_type_visibility = $2)`,
		productID, customerType,
	).Scan(
		&out.ID, &out.ShopID, &out.Name, &out.Slug, &out.Description, &out.ProductType, &out.PriceAmount,
		&out.Currency, &out.Status, &out.OccasionTags, &out.CustomerTypeVisibility,
		&out.PointsDisplayEnabled, &out.PrepMinutes, &out.CreatedAt, &out.UpdatedAt, &out.ImageURL,
		&length, &width, &height, &distanceUnit, &weight, &massUnit,
		&out.Shop.ID, &out.Shop.Name, &out.Shop.Slug, &out.Shop.ImageURL, &out.Shop.Location,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductNotFound
	}
	if err != nil {
		return nil, err
	}
	out.Parcel = models.ProductParcelFromNullable(length, width, height, distanceUnit, weight, massUnit)
	if out.OccasionTags == nil {
		out.OccasionTags = []string{}
	}
	if err := r.attachMedia(ctx, []*models.Product{&out.Product}); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *ProductRepository) CreateInventory(ctx context.Context, inv *models.Inventory) error {
	if inv.UnavailableDates == nil {
		inv.UnavailableDates = []time.Time{}
	}
	err := r.db.QueryRow(ctx, `
		insert into seller.inventory (
			product_id, available_qty, reserved_qty, low_stock_threshold, unavailable_dates
		) values ($1,$2,$3,$4,$5)
		returning id, updated_at`,
		inv.ProductID, inv.AvailableQty, inv.ReservedQty, inv.LowStockThreshold, inv.UnavailableDates,
	).Scan(&inv.ID, &inv.UpdatedAt)
	return err
}

func (r *ProductRepository) GetInventoryByProductID(ctx context.Context, productID string) (*models.Inventory, error) {
	inv := &models.Inventory{}
	err := r.db.QueryRow(ctx, `
		select id, product_id, available_qty, reserved_qty, low_stock_threshold,
		       unavailable_dates, updated_at
		from seller.inventory
		where product_id = $1`, productID,
	).Scan(
		&inv.ID, &inv.ProductID, &inv.AvailableQty, &inv.ReservedQty, &inv.LowStockThreshold,
		&inv.UnavailableDates, &inv.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInventoryNotFound
	}
	if inv.UnavailableDates == nil {
		inv.UnavailableDates = []time.Time{}
	}
	return inv, err
}

func (r *ProductRepository) UpdateInventory(ctx context.Context, inv *models.Inventory) error {
	if inv.UnavailableDates == nil {
		inv.UnavailableDates = []time.Time{}
	}
	err := r.db.QueryRow(ctx, `
		update seller.inventory
		set available_qty = $2,
		    reserved_qty = $3,
		    low_stock_threshold = $4,
		    unavailable_dates = $5,
		    updated_at = now()
		where product_id = $1
		returning id, updated_at`,
		inv.ProductID, inv.AvailableQty, inv.ReservedQty, inv.LowStockThreshold, inv.UnavailableDates,
	).Scan(&inv.ID, &inv.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInventoryNotFound
	}
	return err
}

func (r *ProductRepository) UpsertInventory(ctx context.Context, inv *models.Inventory) error {
	if inv.UnavailableDates == nil {
		inv.UnavailableDates = []time.Time{}
	}
	err := r.db.QueryRow(ctx, `
		insert into seller.inventory (
			product_id, available_qty, reserved_qty, low_stock_threshold, unavailable_dates
		) values ($1,$2,$3,$4,$5)
		on conflict (product_id) do update set
			available_qty = excluded.available_qty,
			reserved_qty = excluded.reserved_qty,
			low_stock_threshold = excluded.low_stock_threshold,
			unavailable_dates = excluded.unavailable_dates,
			updated_at = now()
		returning id, updated_at`,
		inv.ProductID, inv.AvailableQty, inv.ReservedQty, inv.LowStockThreshold, inv.UnavailableDates,
	).Scan(&inv.ID, &inv.UpdatedAt)
	return err
}

func mapProductWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrProductDuplicate
	}
	return err
}

// insertProductMediaTx creates media_assets rows and links them to the product in order.
func insertProductMediaTx(ctx context.Context, tx pgx.Tx, productID uuid.UUID, assets []models.MediaAsset) error {
	for i := range assets {
		if err := insertMediaAssetTx(ctx, tx, &assets[i]); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into seller.product_media (product_id, media_asset_id, position)
			values ($1,$2,$3)`, productID, assets[i].ID, i); err != nil {
			return err
		}
	}
	return nil
}

// attachMedia loads seller.product_media + media_assets onto each product (ordered by position).
func (r *ProductRepository) attachMedia(ctx context.Context, products []*models.Product) error {
	if len(products) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(products))
	byID := make(map[uuid.UUID]*models.Product, len(products))
	for _, p := range products {
		if p == nil {
			continue
		}
		p.Media = []models.ProductMediaItem{}
		ids = append(ids, p.ID)
		byID[p.ID] = p
	}
	if len(ids) == 0 {
		return nil
	}

	rows, err := r.db.Query(ctx, `
		select pm.product_id, pm.media_asset_id, pm.position,
		       a.asset_type, a.bucket, a.object_path, a.cdn_url, a.mime_type, a.size_bytes, a.metadata
		from seller.product_media pm
		inner join media.media_assets a on a.id = pm.media_asset_id
		where pm.product_id = any($1)
		order by pm.product_id, pm.position`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var productID uuid.UUID
		var item models.ProductMediaItem
		if err := rows.Scan(
			&productID, &item.MediaAssetID, &item.Position,
			&item.AssetType, &item.Bucket, &item.ObjectPath, &item.CDNURL,
			&item.MimeType, &item.SizeBytes, &item.Metadata,
		); err != nil {
			return err
		}
		if p, ok := byID[productID]; ok {
			p.Media = append(p.Media, item)
		}
	}
	return rows.Err()
}
