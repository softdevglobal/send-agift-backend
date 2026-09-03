package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Shipment maps to marketplace.shipments.
type Shipment struct {
	ID                       uuid.UUID       `json:"id"`
	OrderID                  uuid.UUID       `json:"order_id"`
	SellerID                 uuid.UUID       `json:"seller_id"`
	CourierProvider          *string         `json:"courier_provider,omitempty"`
	TrackingNumber           *string         `json:"tracking_number,omitempty"`
	LabelMediaID             *uuid.UUID      `json:"label_media_id,omitempty"`
	DeliveryMode             string          `json:"delivery_mode"`
	Status                   string          `json:"status"`
	ProofOfDeliveryMediaID   *uuid.UUID      `json:"proof_of_delivery_media_id,omitempty"`
	DeliveredAt              *time.Time      `json:"delivered_at,omitempty"`
	ProviderShipmentID       *string         `json:"provider_shipment_id,omitempty"`
	ProviderTrackingURL      *string         `json:"provider_tracking_url,omitempty"`
	ProviderMetadata         json.RawMessage `json:"provider_metadata,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
}
