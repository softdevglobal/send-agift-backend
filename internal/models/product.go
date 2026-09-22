package models

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Product maps to seller.products.
type Product struct {
	ID                     uuid.UUID `json:"id"`
	ShopID                 uuid.UUID `json:"shop_id"`
	Name                   string    `json:"name"`
	Slug                   string    `json:"slug"`
	Description            *string   `json:"description,omitempty"`
	ProductType            string    `json:"product_type"`
	PriceAmount            int       `json:"price_amount"`
	Currency               string    `json:"currency"`
	Status                 string    `json:"status"`
	OccasionTags           []string  `json:"occasion_tags"`
	CustomerTypeVisibility string    `json:"customer_type_visibility"`
	PointsDisplayEnabled   bool      `json:"points_display_enabled"`
	PrepMinutes            int       `json:"prep_minutes"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
	// ImageURL is the cover thumbnail (list cards, order lines). Prefer Media for galleries.
	ImageURL *string `json:"image_url,omitempty"`
	// Shipping parcel used for delivery quotes / rates (seller form dims).
	Parcel *ProductParcel `json:"parcel,omitempty"`
	// Media is the ordered gallery (images + videos) from seller.product_media.
	Media []ProductMediaItem `json:"media,omitempty"`
}

// ProductMediaItem is one gallery file joined with media.media_assets.
type ProductMediaItem struct {
	MediaAssetID uuid.UUID       `json:"media_asset_id"`
	Position     int             `json:"position"`
	AssetType    string          `json:"asset_type"` // image | video
	Bucket       string          `json:"bucket"`
	ObjectPath   string          `json:"object_path"`
	CDNURL       *string         `json:"cdn_url,omitempty"`
	MimeType     string          `json:"mime_type"`
	SizeBytes    int64           `json:"size_bytes"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
}

// ProductParcel is the package size/weight stored on a product.
type ProductParcel struct {
	Length       string `json:"length"`
	Width        string `json:"width"`
	Height       string `json:"height"`
	DistanceUnit string `json:"distance_unit"`
	Weight       string `json:"weight"`
	MassUnit     string `json:"mass_unit"`
}

// ProductParcelFromNullable builds a parcel when any dimension/weight is set.
func ProductParcelFromNullable(length, width, height, distanceUnit, weight, massUnit *string) *ProductParcel {
	val := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	l, w, h := val(length), val(width), val(height)
	du, wt, mu := val(distanceUnit), val(weight), val(massUnit)
	if l == "" && w == "" && h == "" && wt == "" {
		return nil
	}
	return &ProductParcel{
		Length: l, Width: w, Height: h,
		DistanceUnit: du, Weight: wt, MassUnit: mu,
	}
}

// Inventory maps to seller.inventory.
type Inventory struct {
	ID                uuid.UUID  `json:"id"`
	ProductID         uuid.UUID  `json:"product_id"`
	AvailableQty      int        `json:"available_qty"`
	ReservedQty       int        `json:"reserved_qty"`
	LowStockThreshold int        `json:"low_stock_threshold"`
	UnavailableDates  []time.Time `json:"unavailable_dates"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// ProductDetails is a product with its inventory row.
type ProductDetails struct {
	Product
	Inventory *Inventory `json:"inventory,omitempty"`
}

// ProductShopSummary is the "sold by" block on a public product page.
type ProductShopSummary struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Slug     string    `json:"slug"`
	ImageURL *string   `json:"image_url,omitempty"`
	Location *string   `json:"customer_visible_location,omitempty"`
}

// PublicProduct is a published product plus its shop, for customer-facing product pages.
type PublicProduct struct {
	Product
	Shop ProductShopSummary `json:"shop"`
}
