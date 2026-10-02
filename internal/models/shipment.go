package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Shipment maps to marketplace.shipments.
type Shipment struct {
	ID                     uuid.UUID       `json:"id"`
	OrderID                uuid.UUID       `json:"order_id"`
	OrderItemID            *uuid.UUID      `json:"order_item_id,omitempty"`
	SellerID               uuid.UUID       `json:"seller_id"`
	IsInternational        bool            `json:"is_international"`
	CustomsDeclaration     json.RawMessage `json:"customs_declaration,omitempty"`
	CourierProvider        *string         `json:"courier_provider,omitempty"`
	TrackingNumber         *string         `json:"tracking_number,omitempty"`
	LabelMediaID           *uuid.UUID      `json:"label_media_id,omitempty"`
	DeliveryMode           string          `json:"delivery_mode"`
	Status                 string          `json:"status"`
	ProofOfDeliveryMediaID *uuid.UUID      `json:"proof_of_delivery_media_id,omitempty"`
	DeliveredAt            *time.Time      `json:"delivered_at,omitempty"`
	ProviderShipmentID     *string         `json:"provider_shipment_id,omitempty"`
	ProviderCustomsID      *string         `json:"provider_customs_declaration_id,omitempty"`
	ProviderTrackingURL    *string         `json:"provider_tracking_url,omitempty"`
	ProviderMetadata       json.RawMessage `json:"provider_metadata,omitempty"`
	// Seller delivery zone snapshot (local delivery / seller's own courier).
	DistanceKm            *float64   `json:"distance_km,omitempty"`
	ZoneMaxKm             *float64   `json:"zone_max_km,omitempty"`
	PriceAmount           *int       `json:"price_amount,omitempty"` // minor units; 0 = free
	Currency              *string    `json:"currency,omitempty"`
	EstimatedDays         *int       `json:"estimated_days,omitempty"`
	EstimatedDeliveryDate *time.Time `json:"estimated_delivery_date,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}
