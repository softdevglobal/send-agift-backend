package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

// AvailabilityRepository loads the shop locations, delivery zones, and
// published-gift stock that the Find gifts search checks.
type AvailabilityRepository struct {
	db *pgxpool.Pool
}

func NewAvailabilityRepository(db *pgxpool.Pool) *AvailabilityRepository {
	return &AvailabilityRepository{db: db}
}

// ShopForAvailability is an active shop and the zones it can deliver inside.
type ShopForAvailability struct {
	ID        uuid.UUID
	Name      string
	Latitude  *float64
	Longitude *float64
	Zones     []models.ShopDeliveryZone
}

// GiftStock is one published gift and the stock row used to decide if it
// can be sent on the searched day.
type GiftStock struct {
	ID                uuid.UUID
	ShopID            uuid.UUID
	Name              string
	Slug              string
	Description       *string
	PriceAmount       int
	Currency          string
	ImageURL          *string
	OccasionTags      []string
	AvailableQty      int
	ReservedQty       int
	LowStockThreshold int
	HasInventory      bool
	UnavailableDates  []time.Time
}

// ListShopsInReach returns active shops whose farthest delivery zone might
// cover destLat/destLng. A bounding box in SQL drops the rest before any
// exact distance check. Zones on each shop are ordered by max_km ascending.
func (r *AvailabilityRepository) ListShopsInReach(ctx context.Context, destLat, destLng float64) ([]ShopForAvailability, error) {
	// 111 km is one degree of latitude. Longitude degrees shrink by cos(latitude).
	// The box is a little larger than the circle, so a shop the exact check would
	// keep is not dropped here. Shops with no coordinates or no zones are skipped.
	rows, err := r.db.Query(ctx, `
		with origins as (
			select s.id, s.name,
			       coalesce(s.latitude, sa.latitude)::float8 as lat,
			       coalesce(s.longitude, sa.longitude)::float8 as lng
			from seller.shops s
			left join seller.seller_addresses sa on sa.id = coalesce(s.address_id, s.return_address_id)
			where s.status = 'active'
			  and coalesce(s.latitude, sa.latitude) is not null
			  and coalesce(s.longitude, sa.longitude) is not null
		),
		reach as (
			select shop_id, max(max_km)::float8 as farthest_km
			from seller.shop_delivery_zones
			group by shop_id
		)
		select o.id, o.name, o.lat, o.lng
		from origins o
		inner join reach r on r.shop_id = o.id
		where abs(o.lat - $1) <= (r.farthest_km / 111.0) * 1.02
		  and least(abs(o.lng - $2), 360 - abs(o.lng - $2))
		      <= (r.farthest_km / greatest(111.0 * abs(cos(radians($1))), 1.0)) * 1.02
		order by o.name asc`, destLat, destLng)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	shops := []ShopForAvailability{}
	index := map[uuid.UUID]int{}
	for rows.Next() {
		var shop ShopForAvailability
		if err := rows.Scan(&shop.ID, &shop.Name, &shop.Latitude, &shop.Longitude); err != nil {
			return nil, err
		}
		shop.Zones = []models.ShopDeliveryZone{}
		index[shop.ID] = len(shops)
		shops = append(shops, shop)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(shops) == 0 {
		return shops, nil
	}

	ids := make([]uuid.UUID, len(shops))
	for i := range shops {
		ids[i] = shops[i].ID
	}
	zoneRows, err := r.db.Query(ctx, `
		select z.id, z.shop_id, z.max_km::float8, z.price_amount, z.currency, z.estimated_days, z.created_at, z.updated_at
		from seller.shop_delivery_zones z
		where z.shop_id = any($1)
		order by z.shop_id, z.max_km asc`, ids)
	if err != nil {
		return nil, err
	}
	defer zoneRows.Close()
	for zoneRows.Next() {
		var z models.ShopDeliveryZone
		if err := zoneRows.Scan(&z.ID, &z.ShopID, &z.MaxKm, &z.PriceAmount, &z.Currency, &z.EstimatedDays, &z.CreatedAt, &z.UpdatedAt); err != nil {
			return nil, err
		}
		z.IsFree = z.PriceAmount == 0
		if i, ok := index[z.ShopID]; ok {
			shops[i].Zones = append(shops[i].Zones, z)
		}
	}
	return shops, zoneRows.Err()
}

// ListPublishedGifts returns published gifts for these shops only, with stock when a row exists.
func (r *AvailabilityRepository) ListPublishedGifts(ctx context.Context, shopIDs []uuid.UUID, customerType string) ([]GiftStock, error) {
	if len(shopIDs) == 0 {
		return []GiftStock{}, nil
	}
	rows, err := r.db.Query(ctx, `
		select p.id, p.shop_id, p.name, p.slug, p.description, p.price_amount, p.currency, p.image_url,
		       p.occasion_tags,
		       i.available_qty, i.reserved_qty, i.low_stock_threshold, i.unavailable_dates
		from seller.products p
		inner join seller.shops s on s.id = p.shop_id
		left join seller.inventory i on i.product_id = p.id
		where p.shop_id = any($1)
		  and s.status = 'active'
		  and p.status = 'published'
		  and (p.customer_type_visibility = 'both' or p.customer_type_visibility = $2)
		order by p.created_at desc`, shopIDs, customerType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []GiftStock{}
	for rows.Next() {
		var item GiftStock
		var available, reserved, threshold *int
		var blocked []time.Time
		if err := rows.Scan(
			&item.ID, &item.ShopID, &item.Name, &item.Slug, &item.Description, &item.PriceAmount, &item.Currency, &item.ImageURL,
			&item.OccasionTags, &available, &reserved, &threshold, &blocked,
		); err != nil {
			return nil, err
		}
		if item.OccasionTags == nil {
			item.OccasionTags = []string{}
		}
		if available != nil {
			item.HasInventory = true
			item.AvailableQty = *available
			if reserved != nil {
				item.ReservedQty = *reserved
			}
			if threshold != nil {
				item.LowStockThreshold = *threshold
			}
			item.UnavailableDates = blocked
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
