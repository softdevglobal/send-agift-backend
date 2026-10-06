ALTER TABLE seller.sellers
    DROP COLUMN IF EXISTS email_code_attempts,
    DROP COLUMN IF EXISTS email_code_sent_at,
    DROP COLUMN IF EXISTS email_code_expires_at,
    DROP COLUMN IF EXISTS email_code_hash,
    DROP COLUMN IF EXISTS email_verified_at;
