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
	// Points the sender attached to the gift, and where they are:
	// none | held (taken from the sender, not yet delivered) | delivered
	// (reached the recipient's account) | returned (back to the sender) |
	// reversed (taken back after a refund).
	GiftPoints       int64     `json:"gift_points"`
	GiftPointsStatus string    `json:"gift_points_status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// OrderItemTracking is the part of a shipment a customer is allowed to see:
// who is carrying the parcel and how to follow it. Deliberately not the whole
// shipment row — the label PDF, provider ids, parcel dimensions and customs
// paperwork are the seller's business, not the buyer's.
type OrderItemTracking struct {
	// Carrier name, e.g. "USPS", or the seller's own courier when they
	// arranged delivery themselves.
	CourierProvider *string `json:"courier_provider,omitempty"`
	TrackingNumber  *string `json:"tracking_number,omitempty"`
	// The carrier's own tracking page. Absent for some seller-arranged
	// shipments, where the number is all the customer gets.
	TrackingURL *string `json:"tracking_url,omitempty"`
	// Shipment progress: label_created | collected | in_transit | delivered |
	// failed | returned.
	Status string `json:"status"`
	// "courier" for a bought carrier label, "seller_managed" when the seller
	// shipped it themselves outside the carrier integration.
	DeliveryMode string     `json:"delivery_mode"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
	ShippedAt    time.Time  `json:"shipped_at"`
}

// OrderItem maps to marketplace.order_items — one product from one shop.
type OrderItem struct {
	ID               uuid.UUID `json:"id"`
	OrderID          uuid.UUID `json:"order_id"`
	SellerID         uuid.UUID `json:"seller_id"`
	ShopID           uuid.UUID `json:"shop_id"`
	ProductID        uuid.UUID `json:"product_id"`
	Quantity         int       `json:"quantity"`
	UnitAmount       int       `json:"unit_amount"`
	TotalAmount      int       `json:"total_amount"`
	FulfilmentStatus string    `json:"fulfilment_status"`
	// The product's reward points as sold, and where they are:
	// none | reserved (held from the seller until delivery) | awarded |
	// released (back to the seller, line cancelled) | reversed (taken back
	// after a refund).
	RewardPointsPerUnit int       `json:"reward_points_per_unit"`
	RewardPoints        int64     `json:"reward_points"`
	RewardStatus        string    `json:"reward_status"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	// Present once the line has actually been shipped.
	Tracking *OrderItemTracking `json:"tracking,omitempty"`
}

// OrderShopDelivery maps to marketplace.order_shop_deliveries: the delivery the
// customer chose and paid for one shop's parcel at checkout.
type OrderShopDelivery struct {
	ShopID   uuid.UUID `json:"shop_id"`
	ShopName string    `json:"shop_name,omitempty"`
	SellerID uuid.UUID `json:"seller_id"`
	// "courier" or "seller_delivery".
	Mode          string   `json:"mode"`
	Provider      string   `json:"provider"`
	ServiceName   string   `json:"service_name"`
	Amount        int      `json:"amount"`
	Currency      string   `json:"currency"`
	EstimatedDays *int     `json:"estimated_days,omitempty"`
	DistanceKm    *float64 `json:"distance_km,omitempty"`
}

// OrderDetails is an order header with its line items.
type OrderDetails struct {
	Order
	Items []OrderItem `json:"items"`
	// One entry per shop priced at checkout. A shop missing here is arranged later.
	ShopDeliveries []OrderShopDelivery `json:"shop_deliveries"`
}

// SellerOrderItemSummary is a seller-scoped order line for list views.
type SellerOrderItemSummary struct {
	OrderItem
	OrderNumber     string    `json:"order_number"`
	OrderStatus     string    `json:"order_status"`
	DeliveryDate    time.Time `json:"delivery_date"`
	ShopName        string    `json:"shop_name"`
	ProductName     string    `json:"product_name"`
	ProductSlug     string    `json:"product_slug"`
	ProductImageURL *string   `json:"product_image_url,omitempty"`
	RecipientName   *string   `json:"recipient_name,omitempty"`
}

// SellerOrderItemDetails is a seller-scoped order line with order, product, and ship-to context.
type SellerOrderItemDetails struct {
	OrderItem
	ShopName string `json:"shop_name"`
	// What the customer paid to deliver this shop's parcel. Nil when the shop
	// was not priced at checkout.
	ShopDelivery    *OrderShopDelivery `json:"shop_delivery"`
	Order           Order              `json:"order"`
	Product         Product            `json:"product"`
	Recipient       *Recipient         `json:"recipient,omitempty"`
	ShippingAddress *RecipientAddress  `json:"shipping_address,omitempty"`
}

// ReceivedGift is a delivered order as the person it was sent to sees it:
// who sent it and what arrived, without what was paid.
type ReceivedGift struct {
	OrderID     uuid.UUID          `json:"order_id"`
	OrderNumber string             `json:"order_number"`
	SenderName  string             `json:"sender_name"`
	GiftMessage *string            `json:"gift_message,omitempty"`
	GiftPoints  int64              `json:"gift_points"`
	DeliveredAt time.Time          `json:"delivered_at"`
	Items       []ReceivedGiftItem `json:"items"`
}

// ReceivedGiftItem is one product in a received gift. ReviewID is set once
// the line has been reviewed, by the recipient or the sender.
type ReceivedGiftItem struct {
	ID               uuid.UUID  `json:"id"`
	ProductID        uuid.UUID  `json:"product_id"`
	ProductName      string     `json:"product_name"`
	ProductSlug      string     `json:"product_slug"`
	ProductImageURL  *string    `json:"product_image_url,omitempty"`
	ShopName         string     `json:"shop_name"`
	Quantity         int        `json:"quantity"`
	FulfilmentStatus string     `json:"fulfilment_status"`
	ReviewID         *uuid.UUID `json:"review_id,omitempty"`
}
