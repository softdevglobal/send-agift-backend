-- Seller delivery zone snapshot on a shipment (local delivery / seller's own courier).
-- Captured when the customer picks seller delivery at checkout, or when the seller
-- starts local delivery / records a manual courier, so later zone edits don't
-- change what was agreed for this order.
ALTER TABLE marketplace.shipments
    ADD COLUMN IF NOT EXISTS distance_km             numeric CHECK (distance_km IS NULL OR distance_km >= 0),
    ADD COLUMN IF NOT EXISTS zone_max_km             numeric CHECK (zone_max_km IS NULL OR zone_max_km > 0),
    ADD COLUMN IF NOT EXISTS price_amount            integer CHECK (price_amount IS NULL OR price_amount >= 0),
    ADD COLUMN IF NOT EXISTS currency                text,
    ADD COLUMN IF NOT EXISTS estimated_days          integer CHECK (estimated_days IS NULL OR estimated_days >= 0),
    ADD COLUMN IF NOT EXISTS estimated_delivery_date date;
