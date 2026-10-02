DROP INDEX IF EXISTS seller.idx_shops_country_id;

ALTER TABLE seller.shops
    DROP COLUMN IF EXISTS country_id;
