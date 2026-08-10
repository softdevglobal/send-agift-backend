-- +migrate Up
-- Align live customer tables with the app schema (columns may be missing if created outside migrations).

ALTER TABLE customer.customers
    ADD COLUMN IF NOT EXISTS phone text;

ALTER TABLE customer.customers
    ADD COLUMN IF NOT EXISTS date_of_birth date;

ALTER TABLE customer.customer_addresses
    ADD COLUMN IF NOT EXISTS latitude numeric;

ALTER TABLE customer.customer_addresses
    ADD COLUMN IF NOT EXISTS longitude numeric;
