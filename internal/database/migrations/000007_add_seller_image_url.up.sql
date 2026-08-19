-- +migrate Up
ALTER TABLE seller.sellers
    ADD COLUMN IF NOT EXISTS image_url text;
