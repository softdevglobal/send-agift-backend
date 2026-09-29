ALTER TABLE marketplace.shipments
    DROP COLUMN IF EXISTS estimated_delivery_date,
    DROP COLUMN IF EXISTS estimated_days,
    DROP COLUMN IF EXISTS currency,
    DROP COLUMN IF EXISTS price_amount,
    DROP COLUMN IF EXISTS zone_max_km,
    DROP COLUMN IF EXISTS distance_km;
