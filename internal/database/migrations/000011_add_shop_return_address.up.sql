-- +migrate Up
ALTER TABLE seller.shops
    ADD COLUMN IF NOT EXISTS return_address_id uuid REFERENCES seller.seller_addresses (id);
