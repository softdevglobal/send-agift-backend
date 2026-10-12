-- Sellers can change the email they sign in with, the same way customers can
-- (000065): a code to the new address, an alert with an undo link to the old
-- one, and a few days with buying points and raising product rewards on hold.

ALTER TABLE seller.sellers
    ADD COLUMN IF NOT EXISTS email_changed_at      timestamptz,
    ADD COLUMN IF NOT EXISTS funding_locked_until  timestamptz,
    ADD COLUMN IF NOT EXISTS sessions_valid_after  timestamptz;

-- One pending change per seller. A new request replaces the old one.
CREATE TABLE IF NOT EXISTS seller.email_change_requests (
    seller_id    uuid PRIMARY KEY REFERENCES seller.sellers (id) ON DELETE CASCADE,
    new_email    citext NOT NULL,
    code_hash    text NOT NULL,
    expires_at   timestamptz NOT NULL,
    sent_at      timestamptz NOT NULL DEFAULT now(),
    attempts     integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    requested_ip text
);

-- Every completed change: the undo record and the fraud trail.
CREATE TABLE IF NOT EXISTS seller.email_history (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id       uuid NOT NULL REFERENCES seller.sellers (id) ON DELETE CASCADE,
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

CREATE INDEX IF NOT EXISTS seller_email_history_seller_idx
    ON seller.email_history (seller_id, changed_at DESC);
CREATE INDEX IF NOT EXISTS seller_email_history_reserved_idx
    ON seller.email_history (old_email) WHERE status = 'active';
