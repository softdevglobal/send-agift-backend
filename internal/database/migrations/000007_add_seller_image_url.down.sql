-- +migrate Down
ALTER TABLE seller.sellers DROP COLUMN IF EXISTS image_url;
