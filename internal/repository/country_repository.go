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
	ErrCountryNotFound  = errors.New("country not found")
	ErrCountryDuplicate = errors.New("country already exists")
)

type CountryRepository struct {
	db *pgxpool.Pool
}

func NewCountryRepository(db *pgxpool.Pool) *CountryRepository {
	return &CountryRepository{db: db}
}

func (r *CountryRepository) List(ctx context.Context) ([]models.Country, error) {
	rows, err := r.db.Query(ctx, `
		select id, iso_code, name, default_currency, default_timezone, status, created_at, updated_at
		from core.countries
		order by name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var countries []models.Country
	for rows.Next() {
		var c models.Country
		if err := rows.Scan(
			&c.ID, &c.ISOCode, &c.Name, &c.DefaultCurrency, &c.DefaultTimezone,
			&c.Status, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		countries = append(countries, c)
	}
	if countries == nil {
		countries = []models.Country{}
	}
	return countries, rows.Err()
}

func (r *CountryRepository) GetByID(ctx context.Context, id string) (*models.Country, error) {
	c := &models.Country{}
	err := r.db.QueryRow(ctx, `
		select id, iso_code, name, default_currency, default_timezone, status, created_at, updated_at
		from core.countries
		where id = $1`, id).Scan(
		&c.ID, &c.ISOCode, &c.Name, &c.DefaultCurrency, &c.DefaultTimezone,
		&c.Status, &c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCountryNotFound
	}
	return c, err
}

func (r *CountryRepository) Create(ctx context.Context, c *models.Country) error {
	err := r.db.QueryRow(ctx, `
		insert into core.countries (iso_code, name, default_currency, default_timezone, status)
		values ($1, $2, $3, $4, $5)
		returning id, created_at, updated_at`,
		c.ISOCode, c.Name, c.DefaultCurrency, c.DefaultTimezone, c.Status,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	return mapCountryWriteError(err)
}

func (r *CountryRepository) Update(ctx context.Context, c *models.Country) error {
	err := r.db.QueryRow(ctx, `
		update core.countries
		set iso_code = $2,
		    name = $3,
		    default_currency = $4,
		    default_timezone = $5,
		    status = $6,
		    updated_at = now()
		where id = $1
		returning updated_at`,
		c.ID, c.ISOCode, c.Name, c.DefaultCurrency, c.DefaultTimezone, c.Status,
	).Scan(&c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCountryNotFound
	}
	return mapCountryWriteError(err)
}

func mapCountryWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrCountryDuplicate
	}
	return err
}

func (r *CountryRepository) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `delete from core.countries where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCountryNotFound
	}
	return nil
}
