package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrCustomerNotFound   = errors.New("customer not found")
	ErrCustomerDuplicate  = errors.New("customer already exists")
	ErrAddressNotFound    = errors.New("address not found")
	ErrSavedGiftNotFound  = errors.New("saved gift not found")
	ErrSavedGiftDuplicate = errors.New("product already saved")
	ErrRecipientNotFound = errors.New("recipient not found")
)

type CustomerRepository struct {
	db *pgxpool.Pool
}

func NewCustomerRepository(db *pgxpool.Pool) *CustomerRepository {
	return &CustomerRepository{db: db}
}

func (r *CustomerRepository) Create(ctx context.Context, c *models.Customer) error {
	err := r.db.QueryRow(ctx, `
		insert into customer.customers (
			country_id, email, phone, password_hash, display_name, customer_type, date_of_birth, status, image_url
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		returning id, created_at, updated_at, image_url`,
		c.CountryID, c.Email, c.Phone, c.PasswordHash, c.DisplayName, c.CustomerType, c.DateOfBirth, c.Status, c.ImageURL,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt, &c.ImageURL)
	return mapCustomerWriteError(err)
}

func (r *CustomerRepository) GetByEmail(ctx context.Context, email string) (*models.Customer, error) {
	c := &models.Customer{}
	err := r.db.QueryRow(ctx, `
		select id, country_id, email, phone, password_hash, display_name, customer_type,
		       date_of_birth, age_verified_at, identity_verified_at, status,
		       created_at, updated_at, deleted_at, image_url
		from customer.customers
		where email = $1 and deleted_at is null and status = 'active'`, email,
	).Scan(
		&c.ID, &c.CountryID, &c.Email, &c.Phone, &c.PasswordHash, &c.DisplayName, &c.CustomerType,
		&c.DateOfBirth, &c.AgeVerifiedAt, &c.IdentityVerifiedAt, &c.Status,
		&c.CreatedAt, &c.UpdatedAt, &c.DeletedAt, &c.ImageURL,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCustomerNotFound
	}
	return c, err
}

func (r *CustomerRepository) GetByID(ctx context.Context, id string) (*models.Customer, error) {
	c := &models.Customer{}
	err := r.db.QueryRow(ctx, `
		select id, country_id, email, phone, password_hash, display_name, customer_type,
		       date_of_birth, age_verified_at, identity_verified_at, status,
		       created_at, updated_at, deleted_at, image_url
		from customer.customers
		where id = $1 and deleted_at is null`, id,
	).Scan(
		&c.ID, &c.CountryID, &c.Email, &c.Phone, &c.PasswordHash, &c.DisplayName, &c.CustomerType,
		&c.DateOfBirth, &c.AgeVerifiedAt, &c.IdentityVerifiedAt, &c.Status,
		&c.CreatedAt, &c.UpdatedAt, &c.DeletedAt, &c.ImageURL,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCustomerNotFound
	}
	return c, err
}

func (r *CustomerRepository) Update(ctx context.Context, c *models.Customer) error {
	err := r.db.QueryRow(ctx, `
		update customer.customers
		set country_id = $2,
		    phone = $3,
		    display_name = $4,
		    customer_type = $5,
		    date_of_birth = $6,
		    status = $7,
		    image_url = $8,
		    updated_at = now()
		where id = $1 and deleted_at is null
		returning updated_at`,
		c.ID, c.CountryID, c.Phone, c.DisplayName, c.CustomerType, c.DateOfBirth, c.Status, c.ImageURL,
	).Scan(&c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCustomerNotFound
	}
	return err
}

func (r *CustomerRepository) SoftDelete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `
		update customer.customers
		set deleted_at = now(), updated_at = now(), status = 'deleted'
		where id = $1 and deleted_at is null`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCustomerNotFound
	}
	return nil
}

func (r *CustomerRepository) CreateAddress(ctx context.Context, a *models.CustomerAddress) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if a.IsDefault {
		if _, err := tx.Exec(ctx, `
			update customer.customer_addresses set is_default = false, updated_at = now()
			where customer_id = $1`, a.CustomerID); err != nil {
			return err
		}
	}

	err = tx.QueryRow(ctx, `
		insert into customer.customer_addresses (
			customer_id, country_id, label, address_type, line1, line2, city, region,
			postal_code, latitude, longitude, is_default
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		returning id, created_at, updated_at`,
		a.CustomerID, a.CountryID, a.Label, a.AddressType, a.Line1, a.Line2, a.City, a.Region,
		a.PostalCode, a.Latitude, a.Longitude, a.IsDefault,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *CustomerRepository) ListAddresses(ctx context.Context, customerID string) ([]models.CustomerAddress, error) {
	rows, err := r.db.Query(ctx, `
		select id, customer_id, country_id, label, address_type, line1, line2, city, region,
		       postal_code, latitude, longitude, is_default, created_at, updated_at
		from customer.customer_addresses
		where customer_id = $1
		order by is_default desc, created_at asc`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.CustomerAddress
	for rows.Next() {
		var a models.CustomerAddress
		if err := rows.Scan(
			&a.ID, &a.CustomerID, &a.CountryID, &a.Label, &a.AddressType, &a.Line1, &a.Line2,
			&a.City, &a.Region, &a.PostalCode, &a.Latitude, &a.Longitude, &a.IsDefault,
			&a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	if items == nil {
		items = []models.CustomerAddress{}
	}
	return items, rows.Err()
}

func (r *CustomerRepository) DeleteAddress(ctx context.Context, customerID, addressID string) error {
	tag, err := r.db.Exec(ctx, `
		delete from customer.customer_addresses
		where id = $1 and customer_id = $2`, addressID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAddressNotFound
	}
	return nil
}

// CreateSavedGift inserts a wishlist row for a customer + product.
// UNIQUE (customer_id, product_id) prevents saving the same product twice.
// There is no Update for saved gifts — change = delete old + create new.
func (r *CustomerRepository) CreateSavedGift(ctx context.Context, g *models.SavedGift) error {
	err := r.db.QueryRow(ctx, `
		insert into customer.saved_gifts (customer_id, product_id)
		values ($1, $2)
		returning id, created_at`,
		g.CustomerID, g.ProductID,
	).Scan(&g.ID, &g.CreatedAt)
	return mapSavedGiftWriteError(err)
}

// ListSavedGifts returns saved gifts for one customer with product details joined from seller.products.
func (r *CustomerRepository) ListSavedGifts(ctx context.Context, customerID string) ([]models.SavedGiftDetails, error) {
	rows, err := r.db.Query(ctx, `
		select
			sg.id, sg.customer_id, sg.product_id, sg.created_at,
			p.id, p.shop_id, p.name, p.slug, p.description, p.product_type, p.price_amount,
			p.currency, p.status, p.occasion_tags, p.customer_type_visibility,
			p.points_display_enabled, p.prep_minutes, p.created_at, p.updated_at, p.image_url
		from customer.saved_gifts sg
		inner join seller.products p on p.id = sg.product_id
		where sg.customer_id = $1
		order by sg.created_at desc`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []models.SavedGiftDetails{}
	for rows.Next() {
		var item models.SavedGiftDetails
		var p models.Product
		if err := rows.Scan(
			&item.ID, &item.CustomerID, &item.ProductID, &item.CreatedAt,
			&p.ID, &p.ShopID, &p.Name, &p.Slug, &p.Description, &p.ProductType, &p.PriceAmount,
			&p.Currency, &p.Status, &p.OccasionTags, &p.CustomerTypeVisibility,
			&p.PointsDisplayEnabled, &p.PrepMinutes, &p.CreatedAt, &p.UpdatedAt, &p.ImageURL,
		); err != nil {
			return nil, err
		}
		if p.OccasionTags == nil {
			p.OccasionTags = []string{}
		}
		item.Product = p
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetSavedGift loads one saved gift owned by this customer (id + customer_id).
func (r *CustomerRepository) GetSavedGift(ctx context.Context, customerID, savedGiftID string) (*models.SavedGift, error) {
	g := &models.SavedGift{}
	err := r.db.QueryRow(ctx, `
		select id, customer_id, product_id, created_at
		from customer.saved_gifts
		where id = $1 and customer_id = $2`, savedGiftID, customerID,
	).Scan(&g.ID, &g.CustomerID, &g.ProductID, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSavedGiftNotFound
	}
	return g, err
}

// DeleteSavedGift removes a saved gift only if it belongs to this customer.
func (r *CustomerRepository) DeleteSavedGift(ctx context.Context, customerID, savedGiftID string) error {
	tag, err := r.db.Exec(ctx, `
		delete from customer.saved_gifts
		where id = $1 and customer_id = $2`, savedGiftID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSavedGiftNotFound
	}
	return nil
}

// CreateRecipient inserts a new row and scans back DB-generated fields.
func (r *CustomerRepository) CreateRecipient(ctx context.Context, rec *models.Recipient) error {
	prefs := rec.Preferences
	if len(prefs) == 0 {
		prefs = json.RawMessage(`{}`)
	}
	return r.db.QueryRow(ctx, `
		insert into customer.recipients (
			customer_id, name, relationship, email, phone, image_url, default_address_id, preferences
		) values ($1,$2,$3,$4,$5,$6,$7,$8)
		returning id, created_at, updated_at`,
		rec.CustomerID, rec.Name, rec.Relationship, rec.Email, rec.Phone, rec.ImageURL, rec.DefaultAddressID, prefs,
	).Scan(&rec.ID, &rec.CreatedAt, &rec.UpdatedAt)
}

// ListRecipients returns every recipient for one customer, newest first.
func (r *CustomerRepository) ListRecipients(ctx context.Context, customerID string) ([]models.Recipient, error) {
	rows, err := r.db.Query(ctx, `
		select id, customer_id, name, relationship, email, phone, image_url,
		       default_address_id, preferences, created_at, updated_at
		from customer.recipients
		where customer_id = $1
		order by created_at desc`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []models.Recipient{}
	for rows.Next() {
		var rec models.Recipient
		if err := rows.Scan(
			&rec.ID, &rec.CustomerID, &rec.Name, &rec.Relationship, &rec.Email, &rec.Phone, &rec.ImageURL,
			&rec.DefaultAddressID, &rec.Preferences, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, rec)
	}
	return items, rows.Err()
}

// GetRecipientByID fetches one recipient, scoped to id AND customer_id.
func (r *CustomerRepository) GetRecipientByID(ctx context.Context, customerID, recipientID string) (*models.Recipient, error) {
	rec := &models.Recipient{}
	err := r.db.QueryRow(ctx, `
		select id, customer_id, name, relationship, email, phone, image_url,
		       default_address_id, preferences, created_at, updated_at
		from customer.recipients
		where id = $1 and customer_id = $2`, recipientID, customerID,
	).Scan(
		&rec.ID, &rec.CustomerID, &rec.Name, &rec.Relationship, &rec.Email, &rec.Phone, &rec.ImageURL,
		&rec.DefaultAddressID, &rec.Preferences, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRecipientNotFound
	}
	return rec, err
}

// UpdateRecipient does a full field overwrite (not a PATCH-style partial update).
func (r *CustomerRepository) UpdateRecipient(ctx context.Context, rec *models.Recipient) error {
	prefs := rec.Preferences
	if len(prefs) == 0 {
		prefs = json.RawMessage(`{}`)
	}
	err := r.db.QueryRow(ctx, `
		update customer.recipients
		set name = $3,
		    relationship = $4,
		    email = $5,
		    phone = $6,
		    image_url = $7,
		    default_address_id = $8,
		    preferences = $9,
		    updated_at = now()
		where id = $1 and customer_id = $2
		returning updated_at`,
		rec.ID, rec.CustomerID, rec.Name, rec.Relationship, rec.Email, rec.Phone,
		rec.ImageURL, rec.DefaultAddressID, prefs,
	).Scan(&rec.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRecipientNotFound
	}
	return err
}

// DeleteRecipient checks RowsAffected instead of relying on an error.
func (r *CustomerRepository) DeleteRecipient(ctx context.Context, customerID, recipientID string) error {
	tag, err := r.db.Exec(ctx, `
		delete from customer.recipients
		where id = $1 and customer_id = $2`, recipientID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRecipientNotFound
	}
	return nil
}

// RecipientAddressBelongsToRecipient is a guard used before letting a
// PUT request set default_address_id.
func (r *CustomerRepository) RecipientAddressBelongsToRecipient(ctx context.Context, recipientID, addressID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx, `
		select exists(
			select 1 from customer.recipient_addresses
			where id = $1 and recipient_id = $2
		)`, addressID, recipientID,
	).Scan(&exists)
	return exists, err
}

// ListRecipientAddresses returns addresses for one recipient, default first.
func (r *CustomerRepository) ListRecipientAddresses(ctx context.Context, recipientID string) ([]models.RecipientAddress, error) {
	rows, err := r.db.Query(ctx, `
		select id, recipient_id, country_id, label, address_type, line1, line2, city, region,
		       postal_code, latitude, longitude, is_default, created_at, updated_at
		from customer.recipient_addresses
		where recipient_id = $1
		order by is_default desc, created_at asc`, recipientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []models.RecipientAddress{}
	for rows.Next() {
		var a models.RecipientAddress
		if err := rows.Scan(
			&a.ID, &a.RecipientID, &a.CountryID, &a.Label, &a.AddressType, &a.Line1, &a.Line2,
			&a.City, &a.Region, &a.PostalCode, &a.Latitude, &a.Longitude, &a.IsDefault,
			&a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, a)
	}
	return items, rows.Err()
}

func (r *CustomerRepository) CreateRecipientAddress(ctx context.Context, a *models.RecipientAddress) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if a.IsDefault {
		if _, err := tx.Exec(ctx, `
			update customer.recipient_addresses set is_default = false, updated_at = now()
			where recipient_id = $1`, a.RecipientID); err != nil {
			return err
		}
	}

	err = tx.QueryRow(ctx, `
		insert into customer.recipient_addresses (
			recipient_id, country_id, label, address_type, line1, line2, city, region,
			postal_code, latitude, longitude, is_default
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		returning id, created_at, updated_at`,
		a.RecipientID, a.CountryID, a.Label, a.AddressType, a.Line1, a.Line2, a.City, a.Region,
		a.PostalCode, a.Latitude, a.Longitude, a.IsDefault,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return err
	}

	if a.IsDefault {
		if _, err := tx.Exec(ctx, `
			update customer.recipients
			set default_address_id = $2, updated_at = now()
			where id = $1`, a.RecipientID, a.ID); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (r *CustomerRepository) GetRecipientAddressByID(ctx context.Context, recipientID, addressID string) (*models.RecipientAddress, error) {
	a := &models.RecipientAddress{}
	err := r.db.QueryRow(ctx, `
		select id, recipient_id, country_id, label, address_type, line1, line2, city, region,
		       postal_code, latitude, longitude, is_default, created_at, updated_at
		from customer.recipient_addresses
		where id = $1 and recipient_id = $2`, addressID, recipientID,
	).Scan(
		&a.ID, &a.RecipientID, &a.CountryID, &a.Label, &a.AddressType, &a.Line1, &a.Line2,
		&a.City, &a.Region, &a.PostalCode, &a.Latitude, &a.Longitude, &a.IsDefault,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAddressNotFound
	}
	return a, err
}

func (r *CustomerRepository) UpdateRecipientAddress(ctx context.Context, a *models.RecipientAddress) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if a.IsDefault {
		if _, err := tx.Exec(ctx, `
			update customer.recipient_addresses set is_default = false, updated_at = now()
			where recipient_id = $1 and id <> $2`, a.RecipientID, a.ID); err != nil {
			return err
		}
	}

	err = tx.QueryRow(ctx, `
		update customer.recipient_addresses
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
		where id = $1 and recipient_id = $2
		returning updated_at`,
		a.ID, a.RecipientID, a.CountryID, a.Label, a.AddressType, a.Line1, a.Line2, a.City,
		a.Region, a.PostalCode, a.Latitude, a.Longitude, a.IsDefault,
	).Scan(&a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAddressNotFound
	}
	if err != nil {
		return err
	}

	if a.IsDefault {
		if _, err := tx.Exec(ctx, `
			update customer.recipients
			set default_address_id = $2, updated_at = now()
			where id = $1`, a.RecipientID, a.ID); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (r *CustomerRepository) DeleteRecipientAddress(ctx context.Context, recipientID, addressID string) error {
	tag, err := r.db.Exec(ctx, `
		delete from customer.recipient_addresses
		where id = $1 and recipient_id = $2`, addressID, recipientID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAddressNotFound
	}
	return nil
}

func mapCustomerWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrCustomerDuplicate
	}
	return err
}

// mapSavedGiftWriteError maps Postgres unique-violation (23505) to ErrSavedGiftDuplicate.
func mapSavedGiftWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrSavedGiftDuplicate
	}
	return err
}

// ParseDate helper for optional YYYY-MM-DD values.
func ParseDate(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
