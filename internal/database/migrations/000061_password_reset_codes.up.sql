-- One live code per account and purpose. "forgot" is the signed-out reset.
-- "profile" is the signed-in change. A new code replaces the previous one.

CREATE TABLE IF NOT EXISTS core.password_reset_codes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_type text NOT NULL CHECK (subject_type IN ('customer', 'seller')),
    subject_id   uuid NOT NULL,
    purpose      text NOT NULL CHECK (purpose IN ('forgot', 'profile')),
    code_hash    text NOT NULL,
    expires_at   timestamptz NOT NULL,
    sent_at      timestamptz NOT NULL DEFAULT now(),
    attempts     integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    UNIQUE (subject_type, subject_id, purpose)
);

CREATE INDEX IF NOT EXISTS password_reset_codes_subject_idx
    ON core.password_reset_codes (subject_type, subject_id);
