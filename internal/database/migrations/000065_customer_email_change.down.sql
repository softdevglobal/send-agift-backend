-- 000065_customer_email_change.down.sql
DROP TABLE IF EXISTS customer.email_history;
DROP TABLE IF EXISTS customer.email_change_requests;
ALTER TABLE customer.customers
    DROP COLUMN IF EXISTS email_changed_at,
    DROP COLUMN IF EXISTS redemption_locked_until,
    DROP COLUMN IF EXISTS sessions_valid_after;
