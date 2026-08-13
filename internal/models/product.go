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
