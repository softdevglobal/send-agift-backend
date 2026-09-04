-- Remove product-level shipping columns (000019) and order-item shipping JSON (000020).
ALTER TABLE seller.products
    DROP COLUMN IF EXISTS origin_country_iso,
    DROP COLUMN IF EXISTS hs_tariff_code,
    DROP COLUMN IF EXISTS height_cm,
    DROP COLUMN IF EXISTS width_cm,
    DROP COLUMN IF EXISTS length_cm,
    DROP COLUMN IF EXISTS weight_grams;

ALTER TABLE marketplace.order_items
    DROP COLUMN IF EXISTS shipping_customs,
    DROP COLUMN IF EXISTS shipping_parcel;

-- Store seller-posted parcel + customs on the shipment row (created at rates, completed at label).
ALTER TABLE marketplace.shipments
    ADD COLUMN IF NOT EXISTS order_item_id uuid REFERENCES marketplace.order_items (id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS is_international boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS parcel_details jsonb,
    ADD COLUMN IF NOT EXISTS customs_declaration jsonb,
    ADD COLUMN IF NOT EXISTS provider_customs_declaration_id text;

CREATE UNIQUE INDEX IF NOT EXISTS idx_shipments_pending_order_item
    ON marketplace.shipments (order_item_id)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_shipments_order_item_id
    ON marketplace.shipments (order_item_id);
