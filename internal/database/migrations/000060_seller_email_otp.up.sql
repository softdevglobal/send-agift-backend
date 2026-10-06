-- A new seller confirms their email with a 6-digit code before they can sign in.
-- Sellers who already had an account are marked confirmed here, so this does
-- not lock anyone out. Business verification_status is left as it is.

ALTER TABLE seller.sellers
    ADD COLUMN IF NOT EXISTS email_verified_at     timestamptz,
    ADD COLUMN IF NOT EXISTS email_code_hash       text,
    ADD COLUMN IF NOT EXISTS email_code_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS email_code_sent_at    timestamptz,
    ADD COLUMN IF NOT EXISTS email_code_attempts   integer NOT NULL DEFAULT 0;

UPDATE seller.sellers
SET email_verified_at = created_at
WHERE email_verified_at IS NULL;
