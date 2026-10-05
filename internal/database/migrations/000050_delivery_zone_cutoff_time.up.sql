-- Same-day bands (estimated_days = 0) need the last time an order still
-- leaves today. Later orders arrive the next day.
ALTER TABLE seller.shop_delivery_zones
    ADD COLUMN IF NOT EXISTS cutoff_time time;

UPDATE seller.shop_delivery_zones
SET cutoff_time = TIME '23:59'
WHERE estimated_days = 0
  AND cutoff_time IS NULL;

ALTER TABLE seller.shop_delivery_zones
    DROP CONSTRAINT IF EXISTS shop_delivery_zones_same_day_cutoff;

ALTER TABLE seller.shop_delivery_zones
    ADD CONSTRAINT shop_delivery_zones_same_day_cutoff
    CHECK (
        (estimated_days = 0 AND cutoff_time IS NOT NULL)
        OR (estimated_days > 0 AND cutoff_time IS NULL)
    );
