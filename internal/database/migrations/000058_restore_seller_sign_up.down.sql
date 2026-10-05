ALTER TABLE seller.sellers
    ADD COLUMN IF NOT EXISTS email_verified_at        timestamptz,
    ADD COLUMN IF NOT EXISTS email_code_hash          text,
    ADD COLUMN IF NOT EXISTS email_code_expires_at    timestamptz,
    ADD COLUMN IF NOT EXISTS email_code_sent_at       timestamptz,
    ADD COLUMN IF NOT EXISTS email_code_attempts      integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS verification_note        text,
    ADD COLUMN IF NOT EXISTS verification_reviewed_at timestamptz,
    ADD COLUMN IF NOT EXISTS verification_reviewed_by uuid REFERENCES admin.admin_users (id) ON DELETE SET NULL;

ALTER TABLE seller.sellers
    ADD CONSTRAINT sellers_verification_status_check
    CHECK (verification_status IN ('unverified', 'pending', 'verified', 'rejected'));

CREATE INDEX IF NOT EXISTS sellers_verification_status_idx
    ON seller.sellers (verification_status, created_at);
