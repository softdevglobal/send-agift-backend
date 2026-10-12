-- Customers sign in with a 6-digit code sent by SMS (textbee) or email, and
-- verify the phone on their profile the same way. A phone only signs in once
-- it has been verified; the phone typed at sign-up was never checked.

ALTER TABLE customer.customers
    ADD COLUMN IF NOT EXISTS phone_e164        text,
    ADD COLUMN IF NOT EXISTS phone_verified_at timestamptz,
    ADD COLUMN IF NOT EXISTS email_verified_at timestamptz;

-- A verified number belongs to one live account.
CREATE UNIQUE INDEX IF NOT EXISTS customers_verified_phone_uq
    ON customer.customers (phone_e164)
    WHERE phone_verified_at IS NOT NULL AND deleted_at IS NULL;

-- One live code per destination and purpose. Sends and wrong tries are
-- counted per day from window_start, so a resend does not reset guesses.
CREATE TABLE IF NOT EXISTS core.login_codes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    channel       text NOT NULL CHECK (channel IN ('email', 'sms')),
    destination   text NOT NULL,
    purpose       text NOT NULL CHECK (purpose IN ('login', 'verify_phone')),
    customer_id   uuid REFERENCES customer.customers (id) ON DELETE CASCADE,
    code_hash     text NOT NULL DEFAULT '',
    expires_at    timestamptz NOT NULL,
    sent_at       timestamptz NOT NULL DEFAULT now(),
    window_start  timestamptz NOT NULL DEFAULT now(),
    send_count    integer NOT NULL DEFAULT 1 CHECK (send_count >= 0),
    attempts      integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    UNIQUE (channel, destination, purpose)
);

-- Every SMS is written here first and sent by a background worker,
-- like core.email_outbox.
CREATE TABLE IF NOT EXISTS core.sms_outbox (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            text NOT NULL,
    dedupe_key      text NOT NULL UNIQUE,
    to_phone        text NOT NULL,
    body            text NOT NULL,
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'sent', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz
);

CREATE INDEX IF NOT EXISTS sms_outbox_pending_idx
    ON core.sms_outbox (next_attempt_at)
    WHERE status = 'pending';
