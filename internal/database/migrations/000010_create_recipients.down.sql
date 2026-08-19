ALTER TABLE customer.recipients
    DROP CONSTRAINT IF EXISTS recipients_default_address_id_fkey;

DROP TABLE IF EXISTS customer.recipient_addresses;
DROP TABLE IF EXISTS customer.recipients;
