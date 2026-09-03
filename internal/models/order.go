package models

import (
	"time"

	"github.com/google/uuid"
)

// Order maps to marketplace.orders — one checkout, one header row.
type Order struct {
	ID              uuid.UUID  `json:"id"`
	OrderNumber     string     `json:"order_number"`
	CustomerID      uuid.UUID  `json:"customer_id"`
	RecipientID     *uuid.UUID `json:"recipient_id,omitempty"`
	CountryID       uuid.UUID  `json:"country_id"`
	CustomerType    string     `json:"customer_type"`
	DeliveryDate    time.Time  `json:"delivery_date"`
	Status          string     `json:"status"`
	SubtotalAmount  int        `json:"subtotal_amount"`
	DeliveryAmount  int        `json:"delivery_amount"`
	TotalAmount     int        `json:"total_amount"`
	Currency        string     `json:"currency"`
	GiftMessage     *string    `json:"gift_message,omitempty"`
	MediaGreetingID *uuid.UUID `json:"media_greeting_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// OrderItem maps to marketplace.order_items — one product from one shop.
type OrderItem struct {
	ID                uuid.UUID `json:"id"`
	OrderID           uuid.UUID `json:"order_id"`
	SellerID          uuid.UUID `json:"seller_id"`
	ShopID            uuid.UUID `json:"shop_id"`
	ProductID         uuid.UUID `json:"product_id"`
	Quantity          int       `json:"quantity"`
	UnitAmount        int       `json:"unit_amount"`
	TotalAmount       int       `json:"total_amount"`
	FulfilmentStatus  string    `json:"fulfilment_status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// OrderDetails is an order header with its line items.
type OrderDetails struct {
	Order
	Items []OrderItem `json:"items"`
}

// SellerOrderItemSummary is a seller-scoped order line for list views.
type SellerOrderItemSummary struct {
	OrderItem
	OrderNumber     string    `json:"order_number"`
	OrderStatus     string    `json:"order_status"`
	DeliveryDate    time.Time `json:"delivery_date"`
	ProductName     string    `json:"product_name"`
	ProductSlug     string    `json:"product_slug"`
	ProductImageURL *string   `json:"product_image_url,omitempty"`
	RecipientName   *string   `json:"recipient_name,omitempty"`
}

// SellerOrderItemDetails is a seller-scoped order line with order, product, and ship-to context.
type SellerOrderItemDetails struct {
	OrderItem
	Order           Order             `json:"order"`
	Product         Product           `json:"product"`
	Recipient       *Recipient        `json:"recipient,omitempty"`
	ShippingAddress *RecipientAddress `json:"shipping_address,omitempty"`
}
