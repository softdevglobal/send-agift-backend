package repository

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrSellerNotFound     = errors.New("seller not found")
	ErrSellerDuplicate    = errors.New("seller already exists")
	ErrShopNotFound       = errors.New("shop not found")
	ErrShopDuplicate      = errors.New("shop already exists")
	ErrSellerAddrNotFound = errors.New("seller address not found")
	// ErrSellerClosed is a seller who deleted their own account.
	ErrSellerClosed = errors.New("seller account is closed")
)

type SellerRepository struct {
	db *pgxpool.Pool
}

func NewSellerRepository(db *pgxpool.Pool) *SellerRepository {
	return &SellerRepository{db: db}
}

const sellerColumns = `
	id, country_id, seller_type, legal_name, trading_name, email, phone,
	password_hash, verification_status, status, created_at, updated_at, image_url,
	local_name, registration_status, registration_note, tax_status,
	contact_name, contact_role, contact_job_title,
	authority_confirmed_at, terms_accepted_at, marketing_opt_in,
	email_verified_at, email_code_hash, email_code_expires_at, email_code_sent_at, email_code_attempts`

func scanSeller(row scanner, s *models.Seller) error {
	return row.Scan(
		&s.ID, &s.CountryID, &s.SellerType, &s.LegalName, &s.TradingName, &s.Email, &s.Phone,
		&s.PasswordHash, &s.VerificationStatus, &s.Status, &s.CreatedAt, &s.UpdatedAt, &s.ImageURL,
		&s.LocalName, &s.RegistrationStatus, &s.RegistrationNote, &s.TaxStatus,
		&s.ContactName, &s.ContactRole, &s.ContactJobTitle,
		&s.AuthorityConfirmedAt, &s.TermsAcceptedAt, &s.MarketingOptIn,
		&s.EmailVerifiedAt, &s.EmailCodeHash, &s.EmailCodeExpiresAt, &s.EmailCodeSentAt, &s.EmailCodeAttempts,
	)
}

func (r *SellerRepository) Create(ctx context.Context, s *models.Seller) error {
	return insertSeller(ctx, r.db, s)
}

func insertSeller(ctx context.Context, q querier, s *models.Seller) error {
	err := q.QueryRow(ctx, `
		insert into seller.sellers (
			country_id, seller_type, legal_name, trading_name, email, phone,
			password_hash, verification_status, status, image_url,
			local_name, registration_status, registration_note, tax_status,
			contact_name, contact_role, contact_job_title,
			authority_confirmed_at, terms_accepted_at, marketing_opt_in
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		returning id, verification_status, status, created_at, updated_at`,
		s.CountryID, s.SellerType, s.LegalName, s.TradingName, s.Email, s.Phone,
		s.PasswordHash, s.VerificationStatus, s.Status, s.ImageURL,
		s.LocalName, s.RegistrationStatus, s.RegistrationNote, s.TaxStatus,
		s.ContactName, s.ContactRole, s.ContactJobTitle,
		s.AuthorityConfirmedAt, s.TermsAcceptedAt, s.MarketingOptIn,
	).Scan(&s.ID, &s.VerificationStatus, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	return mapSellerWriteError(err)
}

func (r *SellerRepository) GetByEmail(ctx context.Context, email string) (*models.Seller, error) {
	return r.getByEmail(ctx, email, true)
}

// GetByEmailAny loads a seller regardless of account status. Login uses it
// so a suspended account can be told apart from a wrong password.
func (r *SellerRepository) GetByEmailAny(ctx context.Context, email string) (*models.Seller, error) {
	return r.getByEmail(ctx, email, false)
}

func (r *SellerRepository) getByEmail(ctx context.Context, email string, activeOnly bool) (*models.Seller, error) {
	s := &models.Seller{}
	query := `select ` + sellerColumns + ` from seller.sellers where email = $1`
	if activeOnly {
		query += ` and status = 'active'`
	}
	err := scanSeller(r.db.QueryRow(ctx, query, email), s)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSellerNotFound
	}
	return s, err
}

func (r *SellerRepository) GetByID(ctx context.Context, id string) (*models.Seller, error) {
	s := &models.Seller{}
	err := scanSeller(r.db.QueryRow(ctx, `
		select `+sellerColumns+`
		from seller.sellers
		where id = $1`, id), s)
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

// UpdatePassword stores a new password hash for an active seller.
func (r *SellerRepository) UpdatePassword(ctx context.Context, id, hash string) error {
	tag, err := r.db.Exec(ctx, `
		update seller.sellers
		set password_hash = $2, updated_at = now()
		where id = $1 and status = 'active'`, id, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSellerNotFound
	}
	return nil
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

	if err := insertAddress(ctx, tx, a); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func insertAddress(ctx context.Context, q querier, a *models.SellerAddress) error {
	return q.QueryRow(ctx, `
		insert into seller.seller_addresses (
			seller_id, country_id, label, address_type, line1, line2, city, region,
			postal_code, latitude, longitude, is_default
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		returning id, created_at, updated_at`,
		a.SellerID, a.CountryID, a.Label, a.AddressType, a.Line1, a.Line2, a.City, a.Region,
		a.PostalCode, a.Latitude, a.Longitude, a.IsDefault,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
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

const shopColumns = `
	id, seller_id, country_id, name, slug, description,
	customer_visible_location, status, address_id, return_address_id, created_at, updated_at, image_url,
	latitude, longitude, timezone,
	website, support_email, returns_policy, categories, gift_options, working_days, pickup_enabled`

func scanShop(row scanner, s *models.Shop, extras ...any) error {
	args := []any{
		&s.ID, &s.SellerID, &s.CountryID, &s.Name, &s.Slug, &s.Description,
		&s.CustomerVisibleLocation, &s.Status, &s.AddressID, &s.ReturnAddressID, &s.CreatedAt, &s.UpdatedAt, &s.ImageURL,
		&s.Latitude, &s.Longitude, &s.Timezone,
		&s.Website, &s.SupportEmail, &s.ReturnsPolicy, &s.Categories, &s.GiftOptions, &s.WorkingDays, &s.PickupEnabled,
	}
	args = append(args, extras...)
	return row.Scan(args...)
}

// textArray keeps a nil slice from being written as NULL into a NOT NULL text[] column.
func textArray(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (r *SellerRepository) CreateShop(ctx context.Context, s *models.Shop) error {
	return insertShop(ctx, r.db, s)
}

func insertShop(ctx context.Context, q querier, s *models.Shop) error {
	s.Categories = textArray(s.Categories)
	s.GiftOptions = textArray(s.GiftOptions)
	s.WorkingDays = textArray(s.WorkingDays)
	err := q.QueryRow(ctx, `
		insert into seller.shops (
			seller_id, country_id, name, slug, description,
			customer_visible_location, status, address_id, return_address_id, image_url,
			latitude, longitude, timezone,
			website, support_email, returns_policy, categories, gift_options, working_days, pickup_enabled
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		returning id, status, created_at, updated_at`,
		s.SellerID, s.CountryID, s.Name, s.Slug, s.Description,
		s.CustomerVisibleLocation, s.Status, s.AddressID, s.ReturnAddressID, s.ImageURL,
		s.Latitude, s.Longitude, s.Timezone,
		s.Website, s.SupportEmail, s.ReturnsPolicy, s.Categories, s.GiftOptions, s.WorkingDays, s.PickupEnabled,
	).Scan(&s.ID, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	return mapShopWriteError(err)
}

func (r *SellerRepository) UpdateShop(ctx context.Context, s *models.Shop) error {
	s.Categories = textArray(s.Categories)
	s.GiftOptions = textArray(s.GiftOptions)
	s.WorkingDays = textArray(s.WorkingDays)
	err := r.db.QueryRow(ctx, `
		update seller.shops
		set name = $3,
		    slug = $4,
		    description = $5,
		    customer_visible_location = $6,
		    status = $7,
		    address_id = $8,
		    return_address_id = $9,
		    image_url = $10,
		    latitude = $11,
		    longitude = $12,
		    country_id = $13,
		    timezone = $14,
		    website = $15,
		    support_email = $16,
		    returns_policy = $17,
		    categories = $18,
		    gift_options = $19,
		    working_days = $20,
		    pickup_enabled = $21,
		    updated_at = now()
		where id = $1 and seller_id = $2
		returning updated_at`,
		s.ID, s.SellerID, s.Name, s.Slug, s.Description,
		s.CustomerVisibleLocation, s.Status, s.AddressID, s.ReturnAddressID, s.ImageURL,
		s.Latitude, s.Longitude, s.CountryID, s.Timezone,
		s.Website, s.SupportEmail, s.ReturnsPolicy, s.Categories, s.GiftOptions, s.WorkingDays, s.PickupEnabled,
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
		select `+shopColumns+`
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
		if err := scanShop(rows, &s); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	if items == nil {
		items = []models.Shop{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.attachDeliveryZones(ctx, shopPtrs(items)); err != nil {
		return nil, err
	}
	return items, nil
}

// ListActiveShops returns all shops with status='active' across all sellers.
// Used by customer-facing marketplace browsing.
func (r *SellerRepository) ListActiveShops(ctx context.Context) ([]models.Shop, error) {
	rows, err := r.db.Query(ctx, `
		select `+shopColumns+`,
		       (select verification_status from seller.sellers where id = seller.shops.seller_id)
		from seller.shops
		where status = 'active'
		  and seller_id in (
		    select id from seller.sellers
		    where status = 'active'
		  )
		order by created_at asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []models.Shop{}
	for rows.Next() {
		var s models.Shop
		if err := scanShop(rows, &s, &s.SellerVerificationStatus); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.attachDeliveryZones(ctx, shopPtrs(items)); err != nil {
		return nil, err
	}
	return items, nil
}

// GetActiveShopByID returns one shop with status='active', for public browsing.
// Unlike GetShopByID it is not scoped to a seller, so it must never expose
// draft or suspended shops.
func (r *SellerRepository) GetActiveShopByID(ctx context.Context, shopID string) (*models.Shop, error) {
	s := &models.Shop{}
	err := scanShop(r.db.QueryRow(ctx, `
		select `+shopColumns+`,
		       (select verification_status from seller.sellers where id = seller.shops.seller_id)
		from seller.shops
		where id = $1 and status = 'active'
		  and seller_id in (
		    select id from seller.sellers
		    where status = 'active'
		  )`, shopID), s, &s.SellerVerificationStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrShopNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := r.attachDeliveryZones(ctx, []*models.Shop{s}); err != nil {
		return nil, err
	}
	return s, nil
}

func (r *SellerRepository) GetShopByID(ctx context.Context, sellerID, shopID string) (*models.Shop, error) {
	s := &models.Shop{}
	err := scanShop(r.db.QueryRow(ctx, `
		select `+shopColumns+`
		from seller.shops
		where id = $1 and seller_id = $2`, shopID, sellerID), s)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrShopNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := r.attachDeliveryZones(ctx, []*models.Shop{s}); err != nil {
		return nil, err
	}
	return s, nil
}

// ListDeliveryZones returns a shop's distance bands, cheapest-reach first (smallest max_km).
func (r *SellerRepository) ListDeliveryZones(ctx context.Context, shopID string) ([]models.ShopDeliveryZone, error) {
	rows, err := r.db.Query(ctx, `
		select id, shop_id, max_km, price_amount, currency, estimated_days, to_char(cutoff_time, 'HH24:MI'), created_at, updated_at
		from seller.shop_delivery_zones
		where shop_id = $1
		order by max_km asc`, shopID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeliveryZones(rows)
}

// ReplaceDeliveryZones deletes the shop's bands and inserts the new list.
func (r *SellerRepository) ReplaceDeliveryZones(ctx context.Context, shopID uuid.UUID, zones []models.ShopDeliveryZone) ([]models.ShopDeliveryZone, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `delete from seller.shop_delivery_zones where shop_id = $1`, shopID); err != nil {
		return nil, err
	}
	out, err := insertDeliveryZones(ctx, tx, shopID, zones)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func insertDeliveryZones(ctx context.Context, q querier, shopID uuid.UUID, zones []models.ShopDeliveryZone) ([]models.ShopDeliveryZone, error) {
	out := make([]models.ShopDeliveryZone, 0, len(zones))
	for _, z := range zones {
		saved := z
		saved.ShopID = shopID
		saved.IsFree = saved.PriceAmount == 0
		err := q.QueryRow(ctx, `
			insert into seller.shop_delivery_zones (shop_id, max_km, price_amount, currency, estimated_days, cutoff_time)
			values ($1,$2,$3,$4,$5,$6::time)
			returning id, created_at, updated_at`,
			shopID, saved.MaxKm, saved.PriceAmount, saved.Currency, saved.EstimatedDays, saved.CutoffTime,
		).Scan(&saved.ID, &saved.CreatedAt, &saved.UpdatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, saved)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MaxKm < out[j].MaxKm })
	return out, nil
}

// SellerSignup is everything one registration request writes.
type SellerSignup struct {
	Seller           *models.Seller
	Identifiers      []models.SellerIdentifier
	TaxRegistrations []models.SellerTaxRegistration
	Addresses        []models.SellerAddress
	// Shop is nil when the seller registers without one.
	Shop          *models.Shop
	DeliveryZones []models.ShopDeliveryZone
	// ShopAddressIndex and ShopReturnAddressIndex point into Addresses,
	// because address IDs only exist once those rows are inserted.
	ShopAddressIndex       *int
	ShopReturnAddressIndex *int
}

// CreateSignup writes the seller and everything that belongs to it in one
// transaction. If any insert fails nothing is kept, so the email stays free
// for a corrected retry.
func (r *SellerRepository) CreateSignup(ctx context.Context, in *SellerSignup) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := insertSeller(ctx, tx, in.Seller); err != nil {
		return err
	}
	sellerID := in.Seller.ID

	for i := range in.Identifiers {
		item := &in.Identifiers[i]
		item.SellerID = sellerID
		if err := tx.QueryRow(ctx, `
			insert into seller.seller_identifiers (
				seller_id, country_id, identifier_type, value, value_normalised, authority, jurisdiction
			) values ($1,$2,$3,$4,$5,$6,$7)
			returning id, created_at`,
			item.SellerID, item.CountryID, item.Type, item.Value, item.ValueNormalised, item.Authority, item.Jurisdiction,
		).Scan(&item.ID, &item.CreatedAt); err != nil {
			return err
		}
	}

	for i := range in.TaxRegistrations {
		item := &in.TaxRegistrations[i]
		item.SellerID = sellerID
		if err := tx.QueryRow(ctx, `
			insert into seller.seller_tax_registrations (
				seller_id, country_id, jurisdiction, scheme, tax_number
			) values ($1,$2,$3,$4,$5)
			returning id, created_at`,
			item.SellerID, item.CountryID, item.Jurisdiction, item.Scheme, item.Number,
		).Scan(&item.ID, &item.CreatedAt); err != nil {
			return err
		}
	}

	for i := range in.Addresses {
		in.Addresses[i].SellerID = sellerID
		if err := insertAddress(ctx, tx, &in.Addresses[i]); err != nil {
			return err
		}
	}

	if in.Shop != nil {
		in.Shop.SellerID = sellerID
		if i := in.ShopAddressIndex; i != nil {
			id := in.Addresses[*i].ID
			in.Shop.AddressID = &id
		}
		if i := in.ShopReturnAddressIndex; i != nil {
			id := in.Addresses[*i].ID
			in.Shop.ReturnAddressID = &id
		}
		if err := insertShop(ctx, tx, in.Shop); err != nil {
			return err
		}
		zones, err := insertDeliveryZones(ctx, tx, in.Shop.ID, in.DeliveryZones)
		if err != nil {
			return err
		}
		in.Shop.DeliveryZones = zones
	}

	return tx.Commit(ctx)
}

func (r *SellerRepository) ListIdentifiers(ctx context.Context, sellerID string) ([]models.SellerIdentifier, error) {
	rows, err := r.db.Query(ctx, `
		select id, seller_id, country_id, identifier_type, value, value_normalised, authority, jurisdiction, created_at
		from seller.seller_identifiers
		where seller_id = $1
		order by created_at asc`, sellerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []models.SellerIdentifier{}
	for rows.Next() {
		var item models.SellerIdentifier
		if err := rows.Scan(
			&item.ID, &item.SellerID, &item.CountryID, &item.Type, &item.Value, &item.ValueNormalised,
			&item.Authority, &item.Jurisdiction, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *SellerRepository) ListTaxRegistrations(ctx context.Context, sellerID string) ([]models.SellerTaxRegistration, error) {
	rows, err := r.db.Query(ctx, `
		select id, seller_id, country_id, jurisdiction, scheme, tax_number, created_at
		from seller.seller_tax_registrations
		where seller_id = $1
		order by created_at asc`, sellerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []models.SellerTaxRegistration{}
	for rows.Next() {
		var item models.SellerTaxRegistration
		if err := rows.Scan(
			&item.ID, &item.SellerID, &item.CountryID, &item.Jurisdiction, &item.Scheme, &item.Number, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ShopSlugTaken reports whether any shop, in any status, already uses slug.
func (r *SellerRepository) ShopSlugTaken(ctx context.Context, slug string) (bool, error) {
	var taken bool
	err := r.db.QueryRow(ctx, `select exists(select 1 from seller.shops where slug = $1)`, slug).Scan(&taken)
	return taken, err
}

func (r *SellerRepository) attachDeliveryZones(ctx context.Context, shops []*models.Shop) error {
	if len(shops) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(shops))
	index := make(map[uuid.UUID]*models.Shop, len(shops))
	for _, shop := range shops {
		if shop == nil {
			continue
		}
		shop.DeliveryZones = []models.ShopDeliveryZone{}
		ids = append(ids, shop.ID)
		index[shop.ID] = shop
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := r.db.Query(ctx, `
		select id, shop_id, max_km, price_amount, currency, estimated_days, to_char(cutoff_time, 'HH24:MI'), created_at, updated_at
		from seller.shop_delivery_zones
		where shop_id = any($1)
		order by max_km asc`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	zones, err := scanDeliveryZones(rows)
	if err != nil {
		return err
	}
	for _, z := range zones {
		shop, ok := index[z.ShopID]
		if !ok {
			continue
		}
		shop.DeliveryZones = append(shop.DeliveryZones, z)
	}
	return nil
}

func shopPtrs(shops []models.Shop) []*models.Shop {
	out := make([]*models.Shop, len(shops))
	for i := range shops {
		out[i] = &shops[i]
	}
	return out
}

func scanDeliveryZones(rows pgx.Rows) ([]models.ShopDeliveryZone, error) {
	out := []models.ShopDeliveryZone{}
	for rows.Next() {
		var z models.ShopDeliveryZone
		if err := rows.Scan(&z.ID, &z.ShopID, &z.MaxKm, &z.PriceAmount, &z.Currency, &z.EstimatedDays, &z.CutoffTime, &z.CreatedAt, &z.UpdatedAt); err != nil {
			return nil, err
		}
		z.IsFree = z.PriceAmount == 0
		out = append(out, z)
	}
	return out, rows.Err()
}

// SaveEmailCode stores a new confirmation code and clears the attempt count.
// A seller whose email is already confirmed is left unchanged.
func (r *SellerRepository) SaveEmailCode(ctx context.Context, sellerID uuid.UUID, hash string, expiresAt time.Time) error {
	tag, err := r.db.Exec(ctx, `
		update seller.sellers
		set email_code_hash = $2,
		    email_code_expires_at = $3,
		    email_code_sent_at = now(),
		    email_code_attempts = 0,
		    updated_at = now()
		where id = $1 and email_verified_at is null`, sellerID, hash, expiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSellerNotFound
	}
	return nil
}

// AddEmailCodeAttempt counts one wrong code and returns the new total.
func (r *SellerRepository) AddEmailCodeAttempt(ctx context.Context, sellerID uuid.UUID) (int, error) {
	var attempts int
	err := r.db.QueryRow(ctx, `
		update seller.sellers
		set email_code_attempts = email_code_attempts + 1, updated_at = now()
		where id = $1
		returning email_code_attempts`, sellerID).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrSellerNotFound
	}
	return attempts, err
}

// ConfirmSellerEmail marks the email confirmed and forgets the code.
func (r *SellerRepository) ConfirmSellerEmail(ctx context.Context, sellerID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `
		update seller.sellers
		set email_verified_at = now(),
		    email_code_hash = null,
		    email_code_expires_at = null,
		    email_code_attempts = 0,
		    updated_at = now()
		where id = $1 and email_verified_at is null`, sellerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSellerNotFound
	}
	return nil
}

const adminSellerSelect = `
	select s.id, s.legal_name, s.trading_name, s.email, s.phone, s.seller_type,
	       c.name, s.verification_status, s.status, s.email_verified_at,
	       (select count(*) from seller.shops sh where sh.seller_id = s.id),
	       s.created_at
	from seller.sellers s
	join core.countries c on c.id = s.country_id`

func scanAdminSeller(row scanner, s *models.AdminSellerSummary) error {
	return row.Scan(
		&s.ID, &s.LegalName, &s.TradingName, &s.Email, &s.Phone, &s.SellerType,
		&s.CountryName, &s.VerificationStatus, &s.Status, &s.EmailVerifiedAt,
		&s.ShopCount, &s.CreatedAt,
	)
}

// ListForAdmin returns sellers for the admin screen. status empty means every
// account. query matches email, legal name, or trading name.
func (r *SellerRepository) ListForAdmin(ctx context.Context, status, query string, limit, offset int) ([]models.AdminSellerSummary, int, error) {
	pattern := "%" + escapeLike(query) + "%"
	filtering := query != ""
	var total int
	err := r.db.QueryRow(ctx, `
		select count(*)
		from seller.sellers s
		where ($1 = '' or s.status = $1)
		  and (
		    not $2
		    or s.email ilike $3 escape '\'
		    or s.legal_name ilike $3 escape '\'
		    or coalesce(s.trading_name, '') ilike $3 escape '\'
		  )`, status, filtering, pattern).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(ctx, adminSellerSelect+`
		where ($1 = '' or s.status = $1)
		  and (
		    not $2
		    or s.email ilike $3 escape '\'
		    or s.legal_name ilike $3 escape '\'
		    or coalesce(s.trading_name, '') ilike $3 escape '\'
		  )
		order by s.created_at desc
		limit $4 offset $5`, status, filtering, pattern, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := []models.AdminSellerSummary{}
	for rows.Next() {
		var item models.AdminSellerSummary
		if err := scanAdminSeller(rows, &item); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

// GetForAdmin loads one seller row for the admin screen.
func (r *SellerRepository) GetForAdmin(ctx context.Context, id string) (*models.AdminSellerSummary, error) {
	item := &models.AdminSellerSummary{}
	err := scanAdminSeller(r.db.QueryRow(ctx, adminSellerSelect+` where s.id = $1`, id), item)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSellerNotFound
	}
	if err != nil {
		return nil, err
	}
	return item, nil
}

// SetAccountStatus switches an open seller between active and suspended.
// A seller who closed their own account is left closed.
func (r *SellerRepository) SetAccountStatus(ctx context.Context, id, status string) error {
	tag, err := r.db.Exec(ctx, `
		update seller.sellers
		set status = $2, updated_at = now()
		where id = $1 and status in ('active', 'suspended')`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var current string
	err = r.db.QueryRow(ctx, `select status from seller.sellers where id = $1`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSellerNotFound
	}
	if err != nil {
		return err
	}
	return ErrSellerClosed
}

// SetVerificationStatus records an admin's business check. A seller who closed
// their own account is left unchanged.
func (r *SellerRepository) SetVerificationStatus(ctx context.Context, id, status string) error {
	tag, err := r.db.Exec(ctx, `
		update seller.sellers
		set verification_status = $2, updated_at = now()
		where id = $1 and status <> 'deleted'`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var current string
	err = r.db.QueryRow(ctx, `select status from seller.sellers where id = $1`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSellerNotFound
	}
	if err != nil {
		return err
	}
	return ErrSellerClosed
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
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
