-- +migrate Down
ALTER TABLE seller.shops DROP COLUMN IF EXISTS address_id;
DROP TABLE IF EXISTS seller.shops;
DROP TABLE IF EXISTS seller.seller_addresses;
DROP TABLE IF EXISTS seller.sellers;
