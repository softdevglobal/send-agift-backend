-- 000065_customer_email_change.up.sql
ALTER TABLE customer.customers
    ADD COLUMN IF NOT EXISTS email_changed_at        timestamptz,
    ADD COLUMN IF NOT EXISTS redemption_locked_until timestamptz,
    ADD COLUMN IF NOT EXISTS sessions_valid_after    timestamptz;

-- One pending change per customer. A new request replaces the old one.
CREATE TABLE IF NOT EXISTS customer.email_change_requests (
    customer_id  uuid PRIMARY KEY REFERENCES customer.customers (id) ON DELETE CASCADE,
    new_email    citext NOT NULL,
    code_hash    text NOT NULL,
    expires_at   timestamptz NOT NULL,
    sent_at      timestamptz NOT NULL DEFAULT now(),
    attempts     integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    requested_ip text
);

-- Every completed change. Doubles as the undo record and the fraud trail.
CREATE TABLE IF NOT EXISTS customer.email_history (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     uuid NOT NULL REFERENCES customer.customers (id) ON DELETE CASCADE,
    old_email       citext NOT NULL,
    new_email       citext NOT NULL,
    changed_at      timestamptz NOT NULL DEFAULT now(),
    changed_ip      text,
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'reverted')),
    undo_token_hash text NOT NULL UNIQUE,
    undo_expires_at timestamptz NOT NULL,
    reverted_at     timestamptz,
    flagged         boolean NOT NULL DEFAULT false,
    flag_reason     text
);

CREATE INDEX IF NOT EXISTS email_history_customer_idx
    ON customer.email_history (customer_id, changed_at DESC);
CREATE INDEX IF NOT EXISTS email_history_reserved_idx
    ON customer.email_history (old_email) WHERE status = 'active';
