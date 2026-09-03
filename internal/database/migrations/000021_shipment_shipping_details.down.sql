DROP INDEX IF EXISTS marketplace.idx_shipments_order_item_id;
DROP INDEX IF EXISTS marketplace.idx_shipments_pending_order_item;

ALTER TABLE marketplace.shipments
    DROP COLUMN IF EXISTS provider_customs_declaration_id,
    DROP COLUMN IF EXISTS customs_declaration,
    DROP COLUMN IF EXISTS parcel_details,
    DROP COLUMN IF EXISTS is_international,
    DROP COLUMN IF EXISTS order_item_id;
