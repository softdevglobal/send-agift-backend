-- +migrate Up
-- Seller addresses / shops.address_id are created in 000006_create_sellers_shops.
-- Kept as a no-op so migration order stays valid for all environments.
SELECT 1;
