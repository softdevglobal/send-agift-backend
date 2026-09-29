ALTER TABLE seller.shops
    ADD COLUMN IF NOT EXISTS latitude numeric,
    ADD COLUMN IF NOT EXISTS longitude numeric;

ALTER TABLE seller.shops
    DROP CONSTRAINT IF EXISTS shops_lat_lng_pair;

ALTER TABLE seller.shops
    ADD CONSTRAINT shops_lat_lng_pair CHECK (
        (latitude IS NULL AND longitude IS NULL)
        OR (latitude IS NOT NULL AND longitude IS NOT NULL
            AND latitude BETWEEN -90 AND 90
            AND longitude BETWEEN -180 AND 180)
    );
