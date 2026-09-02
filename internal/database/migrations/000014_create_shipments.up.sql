-- +migrate Up
CREATE TABLE IF NOT EXISTS marketplace.shipments (
    id                          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id                    uuid NOT NULL REFERENCES marketplace.orders (id) ON DELETE CASCADE,
    seller_id                   uuid NOT NULL REFERENCES seller.sellers (id),
    courier_provider            text,
    tracking_number             text,
    label_media_id              uuid,
    delivery_mode               text NOT NULL
                                CHECK (delivery_mode IN ('courier', 'seller_managed', 'pickup')),
    status                      text NOT NULL DEFAULT 'pending'
                                CHECK (status IN (
                                    'pending',
                                    'label_created',
                                    'collected',
                                    'in_transit',
                                    'delivered',
                                    'failed',
                                    'returned'
                                )),
    proof_of_delivery_media_id  uuid,
    delivered_at                timestamptz,
    provider_shipment_id        text,
    provider_tracking_url       text,
    provider_metadata           jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_shipments_order_id
    ON marketplace.shipments (order_id);

CREATE INDEX IF NOT EXISTS idx_shipments_seller_id
    ON marketplace.shipments (seller_id);

CREATE INDEX IF NOT EXISTS idx_shipments_status
    ON marketplace.shipments (status);

CREATE INDEX IF NOT EXISTS idx_shipments_provider_shipment_id
    ON marketplace.shipments (provider_shipment_id);

CREATE INDEX IF NOT EXISTS idx_shipments_tracking_number
    ON marketplace.shipments (tracking_number);
