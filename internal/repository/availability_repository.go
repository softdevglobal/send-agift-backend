package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

// AvailabilityRepository loads one published product per active shop, with the
// shop dispatch address and delivery zones used to decide if gifts can arrive.
type AvailabilityRepository struct {
	db *pgxpool.Pool
}

func NewAvailabilityRepository(db *pgxpool.Pool) *AvailabilityRepository {
	return &AvailabilityRepository{db: db}
}

// ListShopSamples returns each active shop that has a published product.
// The product is the earliest one in the shop and stands in for a Shippo parcel.
func (r *AvailabilityRepository) ListShopSamples(ctx context.Context) ([]models.AvailabilityShop, error) {
	rows, err := r.db.Query(ctx, `
		select distinct on (s.id)
		       s.id,
		       s.name,
		       p.id,
		       p.parcel_length, p.parcel_width, p.parcel_height, p.parcel_distance_unit,
		       p.parcel_weight, p.parcel_mass_unit,
		       coalesce(se.trading_name, s.name, se.legal_name),
		       coalesce(sa.line1, ''), coalesce(sa.line2, ''), coalesce(sa.city, ''),
		       coalesce(sa.region, ''), coalesce(sa.postal_code, ''),
		       coalesce(fc.iso_code, ''),
		       coalesce(s.latitude, sa.latitude)::float8,
		       coalesce(s.longitude, sa.longitude)::float8
		from seller.shops s
		inner join seller.sellers se on se.id = s.seller_id
		inner join seller.products p on p.shop_id = s.id and p.status = 'published'
		left join seller.seller_addresses sa on sa.id = coalesce(s.return_address_id, s.address_id)
		left join core.countries fc on fc.id = sa.country_id
		where s.status = 'active'
		order by s.id, p.created_at asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	shops := make([]models.AvailabilityShop, 0)
	for rows.Next() {
		var shop models.AvailabilityShop
		if err := rows.Scan(
			&shop.ShopID, &shop.ShopName, &shop.ProductID,
			&shop.ParcelLength, &shop.ParcelWidth, &shop.ParcelHeight, &shop.ParcelDistanceUnit,
			&shop.ParcelWeight, &shop.ParcelMassUnit,
			&shop.FromName, &shop.Street1, &shop.Street2, &shop.City,
			&shop.Region, &shop.PostalCode, &shop.CountryISO,
			&shop.Latitude, &shop.Longitude,
		); err != nil {
			return nil, err
		}
		shops = append(shops, shop)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(shops) == 0 {
		return shops, nil
	}

	shopIDs := make([]uuid.UUID, len(shops))
	index := make(map[uuid.UUID]int, len(shops))
	for i := range shops {
		shopIDs[i] = shops[i].ShopID
		index[shops[i].ShopID] = i
	}

	zoneRows, err := r.db.Query(ctx, `
		select id, shop_id, max_km::float8, price_amount, currency, estimated_days, created_at, updated_at
		from seller.shop_delivery_zones
		where shop_id = any($1)
		order by shop_id, max_km asc`, shopIDs)
	if err != nil {
		return nil, err
	}
	defer zoneRows.Close()
	for zoneRows.Next() {
		var zone models.ShopDeliveryZone
		if err := zoneRows.Scan(
			&zone.ID, &zone.ShopID, &zone.MaxKm, &zone.PriceAmount, &zone.Currency,
			&zone.EstimatedDays, &zone.CreatedAt, &zone.UpdatedAt,
		); err != nil {
			return nil, err
		}
		zone.IsFree = zone.PriceAmount == 0
		if i, ok := index[zone.ShopID]; ok {
			shops[i].Zones = append(shops[i].Zones, zone)
		}
	}
	return shops, zoneRows.Err()
}
