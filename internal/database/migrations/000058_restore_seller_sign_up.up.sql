-- Seller email verification and admin approval (added in 000054) were
-- withdrawn: seller.sellers goes back to its original shape, so sign-up works
-- as it did before. The rest of 000054 (email outbox, gift recipient
-- accounts) stays.
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
