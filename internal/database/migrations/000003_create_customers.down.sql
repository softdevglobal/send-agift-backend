-- +migrate Down
DROP TABLE IF EXISTS customer.customer_addresses;
DROP TABLE IF EXISTS customer.customers;
DROP SCHEMA IF EXISTS customer CASCADE;
