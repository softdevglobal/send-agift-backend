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
	ErrCountryCapabilityNotFound  = errors.New("country capability not found")
	ErrCountryCapabilityDuplicate = errors.New("country capability already exists")
)

type CountryCapabilityRepository struct {
	db *pgxpool.Pool
}

func NewCountryCapabilityRepository(db *pgxpool.Pool) *CountryCapabilityRepository {
	return &CountryCapabilityRepository{db: db}
}

const countryCapabilityColumns = `
	id, country_id,
	customer_registration_enabled, seller_registration_enabled, seller_payouts_enabled,
	domestic_delivery_enabled, international_delivery_enabled,
	memberships_enabled, points_earning_enabled, points_usage_enabled,
	skill_competitions_enabled, app_store_available,
	rule_version, created_at, updated_at`

func scanCountryCapability(row pgx.Row) (*models.CountryCapability, error) {
	c := &models.CountryCapability{}
	err := row.Scan(
		&c.ID, &c.CountryID,
		&c.CustomerRegistrationEnabled, &c.SellerRegistrationEnabled, &c.SellerPayoutsEnabled,
		&c.DomesticDeliveryEnabled, &c.InternationalDeliveryEnabled,
		&c.MembershipsEnabled, &c.PointsEarningEnabled, &c.PointsUsageEnabled,
		&c.SkillCompetitionsEnabled, &c.AppStoreAvailable,
		&c.RuleVersion, &c.CreatedAt, &c.UpdatedAt,
	)
	return c, err
}

func (r *CountryCapabilityRepository) List(ctx context.Context) ([]models.CountryCapability, error) {
	rows, err := r.db.Query(ctx, `
		select `+countryCapabilityColumns+`
		from core.country_capabilities
		order by created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.CountryCapability
	for rows.Next() {
		c, err := scanCountryCapability(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *c)
	}
	if items == nil {
		items = []models.CountryCapability{}
	}
	return items, rows.Err()
}

func (r *CountryCapabilityRepository) ListWithCountries(ctx context.Context) ([]models.CountryCapabilityDetails, error) {
	rows, err := r.db.Query(ctx, `
		select
			co.id, co.iso_code, co.name, co.default_currency, co.default_timezone, co.status, co.created_at, co.updated_at,
			cc.id, cc.country_id,
			cc.customer_registration_enabled, cc.seller_registration_enabled, cc.seller_payouts_enabled,
			cc.domestic_delivery_enabled, cc.international_delivery_enabled,
			cc.memberships_enabled, cc.points_earning_enabled, cc.points_usage_enabled,
			cc.skill_competitions_enabled, cc.app_store_available,
			cc.rule_version, cc.created_at, cc.updated_at
		from core.country_capabilities cc
		inner join core.countries co on co.id = cc.country_id
		order by co.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.CountryCapabilityDetails
	for rows.Next() {
		var item models.CountryCapabilityDetails
		err := rows.Scan(
			&item.Country.ID, &item.Country.ISOCode, &item.Country.Name,
			&item.Country.DefaultCurrency, &item.Country.DefaultTimezone, &item.Country.Status,
			&item.Country.CreatedAt, &item.Country.UpdatedAt,
			&item.Capability.ID, &item.Capability.CountryID,
			&item.Capability.CustomerRegistrationEnabled, &item.Capability.SellerRegistrationEnabled,
			&item.Capability.SellerPayoutsEnabled, &item.Capability.DomesticDeliveryEnabled,
			&item.Capability.InternationalDeliveryEnabled, &item.Capability.MembershipsEnabled,
			&item.Capability.PointsEarningEnabled, &item.Capability.PointsUsageEnabled,
			&item.Capability.SkillCompetitionsEnabled, &item.Capability.AppStoreAvailable,
			&item.Capability.RuleVersion, &item.Capability.CreatedAt, &item.Capability.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if items == nil {
		items = []models.CountryCapabilityDetails{}
	}
	return items, rows.Err()
}

func (r *CountryCapabilityRepository) GetDetailsByCountryID(ctx context.Context, countryID string) (*models.CountryCapabilityDetails, error) {
	row := r.db.QueryRow(ctx, `
		select
			co.id, co.iso_code, co.name, co.default_currency, co.default_timezone, co.status, co.created_at, co.updated_at,
			cc.id, cc.country_id,
			cc.customer_registration_enabled, cc.seller_registration_enabled, cc.seller_payouts_enabled,
			cc.domestic_delivery_enabled, cc.international_delivery_enabled,
			cc.memberships_enabled, cc.points_earning_enabled, cc.points_usage_enabled,
			cc.skill_competitions_enabled, cc.app_store_available,
			cc.rule_version, cc.created_at, cc.updated_at
		from core.country_capabilities cc
		inner join core.countries co on co.id = cc.country_id
		where cc.country_id = $1`, countryID)

	var item models.CountryCapabilityDetails
	err := row.Scan(
		&item.Country.ID, &item.Country.ISOCode, &item.Country.Name,
		&item.Country.DefaultCurrency, &item.Country.DefaultTimezone, &item.Country.Status,
		&item.Country.CreatedAt, &item.Country.UpdatedAt,
		&item.Capability.ID, &item.Capability.CountryID,
		&item.Capability.CustomerRegistrationEnabled, &item.Capability.SellerRegistrationEnabled,
		&item.Capability.SellerPayoutsEnabled, &item.Capability.DomesticDeliveryEnabled,
		&item.Capability.InternationalDeliveryEnabled, &item.Capability.MembershipsEnabled,
		&item.Capability.PointsEarningEnabled, &item.Capability.PointsUsageEnabled,
		&item.Capability.SkillCompetitionsEnabled, &item.Capability.AppStoreAvailable,
		&item.Capability.RuleVersion, &item.Capability.CreatedAt, &item.Capability.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCountryCapabilityNotFound
	}
	return &item, err
}

func (r *CountryCapabilityRepository) GetByCountryID(ctx context.Context, countryID string) (*models.CountryCapability, error) {
	row := r.db.QueryRow(ctx, `
		select `+countryCapabilityColumns+`
		from core.country_capabilities
		where country_id = $1`, countryID)
	c, err := scanCountryCapability(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCountryCapabilityNotFound
	}
	return c, err
}

func (r *CountryCapabilityRepository) GetByID(ctx context.Context, id string) (*models.CountryCapability, error) {
	row := r.db.QueryRow(ctx, `
		select `+countryCapabilityColumns+`
		from core.country_capabilities
		where id = $1`, id)
	c, err := scanCountryCapability(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCountryCapabilityNotFound
	}
	return c, err
}

func (r *CountryCapabilityRepository) Create(ctx context.Context, c *models.CountryCapability) error {
	err := r.db.QueryRow(ctx, `
		insert into core.country_capabilities (
			country_id,
			customer_registration_enabled, seller_registration_enabled, seller_payouts_enabled,
			domestic_delivery_enabled, international_delivery_enabled,
			memberships_enabled, points_earning_enabled, points_usage_enabled,
			skill_competitions_enabled, app_store_available,
			rule_version
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		returning id, created_at, updated_at`,
		c.CountryID,
		c.CustomerRegistrationEnabled, c.SellerRegistrationEnabled, c.SellerPayoutsEnabled,
		c.DomesticDeliveryEnabled, c.InternationalDeliveryEnabled,
		c.MembershipsEnabled, c.PointsEarningEnabled, c.PointsUsageEnabled,
		c.SkillCompetitionsEnabled, c.AppStoreAvailable,
		c.RuleVersion,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	return mapCountryCapabilityWriteError(err)
}

func (r *CountryCapabilityRepository) Update(ctx context.Context, c *models.CountryCapability) error {
	err := r.db.QueryRow(ctx, `
		update core.country_capabilities
		set customer_registration_enabled = $2,
		    seller_registration_enabled = $3,
		    seller_payouts_enabled = $4,
		    domestic_delivery_enabled = $5,
		    international_delivery_enabled = $6,
		    memberships_enabled = $7,
		    points_earning_enabled = $8,
		    points_usage_enabled = $9,
		    skill_competitions_enabled = $10,
		    app_store_available = $11,
		    rule_version = $12,
		    updated_at = now()
		where id = $1
		returning updated_at`,
		c.ID,
		c.CustomerRegistrationEnabled, c.SellerRegistrationEnabled, c.SellerPayoutsEnabled,
		c.DomesticDeliveryEnabled, c.InternationalDeliveryEnabled,
		c.MembershipsEnabled, c.PointsEarningEnabled, c.PointsUsageEnabled,
		c.SkillCompetitionsEnabled, c.AppStoreAvailable,
		c.RuleVersion,
	).Scan(&c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCountryCapabilityNotFound
	}
	return mapCountryCapabilityWriteError(err)
}

func mapCountryCapabilityWriteError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrCountryCapabilityDuplicate
	}
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrCountryNotFound
	}
	return err
}

func (r *CountryCapabilityRepository) DeleteByCountryID(ctx context.Context, countryID string) error {
	tag, err := r.db.Exec(ctx, `delete from core.country_capabilities where country_id = $1`, countryID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCountryCapabilityNotFound
	}
	return nil
}

func (r *CountryCapabilityRepository) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `delete from core.country_capabilities where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCountryCapabilityNotFound
	}
	return nil
}
