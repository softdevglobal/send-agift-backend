ALTER TABLE seller.shops DROP CONSTRAINT IF EXISTS shops_lat_lng_pair;
ALTER TABLE seller.shops DROP COLUMN IF EXISTS longitude;
ALTER TABLE seller.shops DROP COLUMN IF EXISTS latitude;
