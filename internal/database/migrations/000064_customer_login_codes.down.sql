DROP TABLE IF EXISTS core.sms_outbox;
DROP TABLE IF EXISTS core.login_codes;
DROP INDEX IF EXISTS customer.customers_verified_phone_uq;

ALTER TABLE customer.customers
    DROP COLUMN IF EXISTS email_verified_at,
    DROP COLUMN IF EXISTS phone_verified_at,
    DROP COLUMN IF EXISTS phone_e164;
