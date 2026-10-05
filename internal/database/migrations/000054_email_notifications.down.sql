DROP INDEX IF EXISTS marketplace.orders_recipient_notice_due_idx;
DROP INDEX IF EXISTS marketplace.orders_recipient_customer_idx;
ALTER TABLE marketplace.orders
    DROP COLUMN IF EXISTS recipient_notified_at,
    DROP COLUMN IF EXISTS recipient_customer_id;

ALTER TABLE customer.customers
    DROP COLUMN IF EXISTS password_change_required;

DROP INDEX IF EXISTS seller.sellers_verification_status_idx;
ALTER TABLE seller.sellers
    DROP CONSTRAINT IF EXISTS sellers_verification_status_check;
ALTER TABLE seller.sellers
    DROP COLUMN IF EXISTS verification_reviewed_by,
    DROP COLUMN IF EXISTS verification_reviewed_at,
    DROP COLUMN IF EXISTS verification_note,
    DROP COLUMN IF EXISTS email_code_attempts,
    DROP COLUMN IF EXISTS email_code_sent_at,
    DROP COLUMN IF EXISTS email_code_expires_at,
    DROP COLUMN IF EXISTS email_code_hash,
    DROP COLUMN IF EXISTS email_verified_at;

DROP TABLE IF EXISTS core.email_outbox;
