package repository

import (
	"context"
	"errors"
	"time"

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

func (r *ProductRepository) Create(ctx context.Context, p *models.Product) error {
	if p.OccasionTags == nil {
		p.OccasionTags = []string{}
	}
	err := r.db.QueryRow(ctx, `
		insert into seller.products (
			shop_id, name, slug, description, product_type, price_amount, currency,
			status, occasion_tags, customer_type_visibility, points_display_enabled, prep_minutes
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		returning id, status, created_at, updated_at`,
		p.ShopID, p.Name, p.Slug, p.Description, p.ProductType, p.PriceAmount, p.Currency,
		p.Status, p.OccasionTags, p.CustomerTypeVisibility, p.PointsDisplayEnabled, p.PrepMinutes,
	).Scan(&p.ID, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	return mapProductWriteError(err)
}

func (r *ProductRepository) Update(ctx context.Context, p *models.Product) error {
	if p.OccasionTags == nil {
		p.OccasionTags = []string{}
	}
	err := r.db.QueryRow(ctx, `
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
		    updated_at = now()
		where id = $1
		returning updated_at`,
		p.ID, p.Name, p.Slug, p.Description, p.ProductType, p.PriceAmount, p.Currency,
		p.Status, p.OccasionTags, p.CustomerTypeVisibility, p.PointsDisplayEnabled, p.PrepMinutes,
	).Scan(&p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProductNotFound
	}
	return mapProductWriteError(err)
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

func (r *ProductRepository) GetByIDForSeller(ctx context.Context, sellerID, productID string) (*models.Product, error) {
	p := &models.Product{}
	err := r.db.QueryRow(ctx, `
		select p.id, p.shop_id, p.name, p.slug, p.description, p.product_type, p.price_amount,
		       p.currency, p.status, p.occasion_tags, p.customer_type_visibility,
		       p.points_display_enabled, p.prep_minutes, p.created_at, p.updated_at
		from seller.products p
		inner join seller.shops s on s.id = p.shop_id
		where p.id = $1 and s.seller_id = $2`, productID, sellerID,
	).Scan(
		&p.ID, &p.ShopID, &p.Name, &p.Slug, &p.Description, &p.ProductType, &p.PriceAmount,
		&p.Currency, &p.Status, &p.OccasionTags, &p.CustomerTypeVisibility,
		&p.PointsDisplayEnabled, &p.PrepMinutes, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProductNotFound
	}
	if p.OccasionTags == nil {
		p.OccasionTags = []string{}
	}
	return p, err
}

func (r *ProductRepository) ListByShopForSeller(ctx context.Context, sellerID, shopID string) ([]models.Product, error) {
	rows, err := r.db.Query(ctx, `
		select p.id, p.shop_id, p.name, p.slug, p.description, p.product_type, p.price_amount,
		       p.currency, p.status, p.occasion_tags, p.customer_type_visibility,
		       p.points_display_enabled, p.prep_minutes, p.created_at, p.updated_at
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
		if err := rows.Scan(
			&p.ID, &p.ShopID, &p.Name, &p.Slug, &p.Description, &p.ProductType, &p.PriceAmount,
			&p.Currency, &p.Status, &p.OccasionTags, &p.CustomerTypeVisibility,
			&p.PointsDisplayEnabled, &p.PrepMinutes, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if p.OccasionTags == nil {
			p.OccasionTags = []string{}
		}
		items = append(items, p)
	}
	return items, rows.Err()
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
