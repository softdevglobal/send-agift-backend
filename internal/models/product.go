package models

import (
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
	ImageURL               *string   `json:"image_url,omitempty"`
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
