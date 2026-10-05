ALTER TABLE seller.shop_delivery_zones
    DROP CONSTRAINT IF EXISTS shop_delivery_zones_same_day_cutoff;

ALTER TABLE seller.shop_delivery_zones
    DROP COLUMN IF EXISTS cutoff_time;
