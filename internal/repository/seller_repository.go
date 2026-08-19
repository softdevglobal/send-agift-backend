package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrSellerNotFound  = errors.New("seller not found")
	ErrSellerDuplicate = errors.New("seller already exists")
	ErrShopNotFound    = errors.New("shop not found")
	ErrShopDuplicate   = errors.New("shop already exists")
	ErrSellerAddrNotFound = errors.New("seller address not found")
)

type SellerRepository struct {
	db *pgxpool.Pool
}

func NewSellerRepository(db *pgxpool.Pool) *SellerRepository {
	return &SellerRepository{db: db}
}

func (r *SellerRepository) Create(ctx context.Context, s *models.Seller) error {
	err := r.db.QueryRow(ctx, `
		insert into seller.sellers (
			country_id, seller_type, legal_name, trading_name, email, phone,
			image_url, password_hash, verification_status, status
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		returning id, verification_status, status, created_at, updated_at`,
		s.CountryID, s.SellerType, s.LegalName, s.TradingName, s.Email, s.Phone,
		s.ImageURL, s.PasswordHash, s.VerificationStatus, s.Status,
	).Scan(&s.ID, &s.VerificationStatus, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	return mapSellerWriteError(err)
}

func (r *SellerRepository) GetByEmail(ctx context.Context, email string) (*models.Seller, error) {
	s := &models.Seller{}
	err := r.db.QueryRow(ctx, `
		select id, country_id, seller_type, legal_name, trading_name, email, phone,
		       image_url, password_hash, verification_status, status, created_at, updated_at
		from seller.sellers
		where email = $1 and status = 'active'`, email,
	).Scan(
		&s.ID, &s.CountryID, &s.SellerType, &s.LegalName, &s.TradingName, &s.Email, &s.Phone,
		&s.ImageURL, &s.PasswordHash, &s.VerificationStatus, &s.Status, &s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSellerNotFound
	}
	return s, err
}

func (r *SellerRepository) GetByID(ctx context.Context, id string) (*models.Seller, error) {
	s := &models.Seller{}
	err := r.db.QueryRow(ctx, `
		select id, country_id, seller_type, legal_name, trading_name, email, phone,
		       image_url, password_hash, verification_status, status, created_at, updated_at
		from seller.sellers
		where id = $1`, id,
	).Scan(
		&s.ID, &s.CountryID, &s.SellerType, &s.LegalName, &s.TradingName, &s.Email, &s.Phone,
		&s.ImageURL, &s.PasswordHash, &s.VerificationStatus, &s.Status, &s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSellerNotFound
	}
	return s, err
}

func (r *SellerRepository) Update(ctx context.Context, s *models.Seller) error {
	err := r.db.QueryRow(ctx, `
		update seller.sellers
		set country_id = $2,
		    seller_type = $3,
		    legal_name = $4,
		    trading_name = $5,
		    phone = $6,
		    image_url = $7,
		    updated_at = now()
		where id = $1
		returning updated_at`,
		s.ID, s.CountryID, s.SellerType, s.LegalName, s.TradingName, s.Phone, s.ImageURL,
	).Scan(&s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSellerNotFound
	}
	return err
}

func (r *SellerRepository) SoftDeactivate(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `
		update seller.sellers set status = 'deleted', updated_at = now()
		where id = $1 and status <> 'deleted'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSellerNotFound
	}
	return nil
}

func (r *SellerRepository) CreateAddress(ctx context.Context, a *models.SellerAddress) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if a.IsDefault {
		if _, err := tx.Exec(ctx, `
			update seller.seller_addresses set is_default = false, updated_at = now()
			where seller_id = $1`, a.SellerID); err != nil {
			return err
		}
	}

	err = tx.QueryRow(ctx, `
		insert into seller.seller_addresses (
			seller_id, country_id, label, address_type, line1, line2, city, region,
			postal_code, latitude, longitude, is_default
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		returning id, created_at, updated_at`,
		a.SellerID, a.CountryID, a.Label, a.AddressType, a.Line1, a.Line2, a.City, a.Region,
		a.PostalCode, a.Latitude, a.Longitude, a.IsDefault,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *SellerRepository) ListAddresses(ctx context.Context, sellerID string) ([]models.SellerAddress, error) {
	rows, err := r.db.Query(ctx, `
		select id, seller_id, country_id, label, address_type, line1, line2, city, region,
		       postal_code, latitude, longitude, is_default, created_at, updated_at
		from seller.seller_addresses
		where seller_id = $1
		order by is_default desc, created_at asc`, sellerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.SellerAddress
	for rows.Next() {
		var a models.SellerAddress
		if err := rows.Scan(
			&a.ID, &a.SellerID, &a.CountryID, &a.Label, &a.AddressType, &a.Line1, &a.Line2,
			&a.City, &a.Region, &a.PostalCode, &a.Latitude, &a.Longitude, &a.IsDefault,
			&a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	if items == nil {
		items = []models.SellerAddress{}
	}
	return items, rows.Err()
}

func (r *SellerRepository) UpdateAddress(ctx context.Context, a *models.SellerAddress) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if a.IsDefault {
		if _, err := tx.Exec(ctx, `
			update seller.seller_addresses set is_default = false, updated_at = now()
			where seller_id = $1 and id <> $2`, a.SellerID, a.ID); err != nil {
			return err
		}
	}

	err = tx.QueryRow(ctx, `
		update seller.seller_addresses
		set country_id = $3,
		    label = $4,
		    address_type = $5,
		    line1 = $6,
		    line2 = $7,
		    city = $8,
		    region = $9,
		    postal_code = $10,
		    latitude = $11,
		    longitude = $12,
		    is_default = $13,
		    updated_at = now()
		where id = $1 and seller_id = $2
		returning created_at, updated_at`,
		a.ID, a.SellerID, a.CountryID, a.Label, a.AddressType, a.Line1, a.Line2, a.City,
		a.Region, a.PostalCode, a.Latitude, a.Longitude, a.IsDefault,
	).Scan(&a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSellerAddrNotFound
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *SellerRepository) DeleteAddress(ctx context.Context, sellerID, addressID string) error {
	// clear shop links first
	if _, err := r.db.Exec(ctx, `
		update seller.shops set address_id = null, updated_at = now()
		where seller_id = $1 and address_id = $2`, sellerID, addressID); err != nil {
		return err
	}
	tag, err := r.db.Exec(ctx, `
		delete from seller.seller_addresses
		where id = $1 and seller_id = $2`, addressID, sellerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSellerAddrNotFound
	}
	return nil
}

func (r *SellerRepository) CreateShop(ctx context.Context, s *models.Shop) error {
	err := r.db.QueryRow(ctx, `
		insert into seller.shops (
			seller_id, name, slug, description, return_address_mode,
			customer_visible_location, status, address_id
		) values ($1,$2,$3,$4,$5,$6,$7,$8)
		returning id, status, created_at, updated_at`,
		s.SellerID, s.Name, s.Slug, s.Description, s.ReturnAddressMode,
		s.CustomerVisibleLocation, s.Status, s.AddressID,
	).Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	return mapShopWriteError(err)
}

func (r *SellerRepository) UpdateShop(ctx context.Context, s *models.Shop) error {
	err := r.db.QueryRow(ctx, `
		update seller.shops
		set name = $3,
		    slug = $4,
		    description = $5,
		    return_address_mode = $6,
		    customer_visible_location = $7,
		    status = $8,
		    address_id = $9,
		    updated_at = now()
		where id = $1 and seller_id = $2
		returning updated_at`,
		s.ID, s.SellerID, s.Name, s.Slug, s.Description, s.ReturnAddressMode,
		s.CustomerVisibleLocation, s.Status, s.AddressID,
	).Scan(&s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrShopNotFound
	}
	return mapShopWriteError(err)
}

func (r *SellerRepository) DeleteShop(ctx context.Context, sellerID, shopID string) error {
	tag, err := r.db.Exec(ctx, `
		delete from seller.shops where id = $1 and seller_id = $2`, shopID, sellerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrShopNotFound
	}
	return nil
}

func (r *SellerRepository) ListShops(ctx context.Context, sellerID string) ([]models.Shop, error) {
	rows, err := r.db.Query(ctx, `
		select id, seller_id, name, slug, description, return_address_mode,
		       customer_visible_location, status, address_id, created_at, updated_at
		from seller.shops
		where seller_id = $1
		order by created_at asc`, sellerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.Shop
	for rows.Next() {
		var s models.Shop
		if err := rows.Scan(
			&s.ID, &s.SellerID, &s.Name, &s.Slug, &s.Description, &s.ReturnAddressMode,
			&s.CustomerVisibleLocation, &s.Status, &s.AddressID, &s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	if items == nil {
		items = []models.Shop{}
	}
	return items, rows.Err()
}

func (r *SellerRepository) GetShopByID(ctx context.Context, sellerID, shopID string) (*models.Shop, error) {
	s := &models.Shop{}
	err := r.db.QueryRow(ctx, `
		select id, seller_id, name, slug, description, return_address_mode,
		       customer_visible_location, status, address_id, created_at, updated_at
		from seller.shops
		where id = $1 and seller_id = $2`, shopID, sellerID,
	).Scan(
		&s.ID, &s.SellerID, &s.Name, &s.Slug, &s.Description, &s.ReturnAddressMode,
		&s.CustomerVisibleLocation, &s.Status, &s.AddressID, &s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrShopNotFound
	}
	return s, err
}

func mapSellerWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSellerDuplicate
	}
	return err
}

func mapShopWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrShopDuplicate
	}
	return err
}
