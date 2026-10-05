-- Transactional email (ZeptoMail), seller email verification and approval,
-- and accounts for gift recipients.
--
--   * core.email_outbox: every email is rendered and written here first, and
--     a background worker sends it. A failed send is retried with backoff,
--     and dedupe_key stops the same event mailing anyone twice.
--   * seller.sellers: a seller confirms their email with a 6-digit code
--     (unverified -> pending), then an admin reviews the account
--     (pending -> verified | rejected). Only a verified seller can open shops
--     and list products.
--   * customer.customers.password_change_required: set on accounts made for
--     gift recipients, which start with a default password.
--   * marketplace.orders: the recipient's account, and when the recipient was
--     told their gift arrived.

CREATE TABLE IF NOT EXISTS core.email_outbox (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            text NOT NULL,
    dedupe_key      text NOT NULL UNIQUE,
    to_email        text NOT NULL,
    to_name         text,
    subject         text NOT NULL,
    html_body       text NOT NULL,
    text_body       text NOT NULL,
    -- pending: waiting to send; sent: accepted by ZeptoMail; failed: gave up.
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz
);

CREATE INDEX IF NOT EXISTS email_outbox_pending_idx
    ON core.email_outbox (next_attempt_at)
    WHERE status = 'pending';

ALTER TABLE seller.sellers
    ADD COLUMN IF NOT EXISTS email_verified_at        timestamptz,
    ADD COLUMN IF NOT EXISTS email_code_hash          text,
    ADD COLUMN IF NOT EXISTS email_code_expires_at    timestamptz,
    ADD COLUMN IF NOT EXISTS email_code_sent_at       timestamptz,
    ADD COLUMN IF NOT EXISTS email_code_attempts      integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS verification_note        text,
    ADD COLUMN IF NOT EXISTS verification_reviewed_at timestamptz,
    ADD COLUMN IF NOT EXISTS verification_reviewed_by uuid REFERENCES admin.admin_users (id) ON DELETE SET NULL;

-- Sellers who signed up before verification existed are already trading:
-- their email counts as confirmed and their account as approved.
UPDATE seller.sellers
SET email_verified_at = created_at,
    verification_status = 'verified',
    verification_reviewed_at = now()
WHERE email_verified_at IS NULL;

ALTER TABLE seller.sellers
    DROP CONSTRAINT IF EXISTS sellers_verification_status_check;
ALTER TABLE seller.sellers
    ADD CONSTRAINT sellers_verification_status_check
    CHECK (verification_status IN ('unverified', 'pending', 'verified', 'rejected'));

CREATE INDEX IF NOT EXISTS sellers_verification_status_idx
    ON seller.sellers (verification_status, created_at);

ALTER TABLE customer.customers
    ADD COLUMN IF NOT EXISTS password_change_required boolean NOT NULL DEFAULT false;

ALTER TABLE marketplace.orders
    ADD COLUMN IF NOT EXISTS recipient_customer_id uuid REFERENCES customer.customers (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS recipient_notified_at timestamptz;

-- Gifts delivered before this release are not announced after the fact.
UPDATE marketplace.orders
SET recipient_notified_at = now()
WHERE status = 'delivered' AND recipient_notified_at IS NULL;

CREATE INDEX IF NOT EXISTS orders_recipient_customer_idx
    ON marketplace.orders (recipient_customer_id)
    WHERE recipient_customer_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS orders_recipient_notice_due_idx
    ON marketplace.orders (updated_at)
    WHERE status = 'delivered' AND recipient_notified_at IS NULL;
