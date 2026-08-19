-- +migrate Down
ALTER TABLE seller.shops DROP COLUMN IF EXISTS return_address_id;
