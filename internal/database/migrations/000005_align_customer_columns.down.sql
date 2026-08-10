-- +migrate Down
ALTER TABLE customer.customer_addresses
    DROP COLUMN IF EXISTS longitude;

ALTER TABLE customer.customer_addresses
    DROP COLUMN IF EXISTS latitude;

ALTER TABLE customer.customers
    DROP COLUMN IF EXISTS date_of_birth;

ALTER TABLE customer.customers
    DROP COLUMN IF EXISTS phone;
