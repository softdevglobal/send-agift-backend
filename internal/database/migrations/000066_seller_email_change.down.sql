DROP TABLE IF EXISTS seller.email_history;
DROP TABLE IF EXISTS seller.email_change_requests;
ALTER TABLE seller.sellers
    DROP COLUMN IF EXISTS email_changed_at,
    DROP COLUMN IF EXISTS funding_locked_until,
    DROP COLUMN IF EXISTS sessions_valid_after;
