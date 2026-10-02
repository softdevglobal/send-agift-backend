-- Push notifications to the mobile app, sent through Firebase Cloud
-- Messaging so they arrive even when the app is closed.
--
--   * customer.push_devices: one row per app install. A token belongs to the
--     customer last signed in on that device, so signing in as someone else
--     moves it rather than duplicating it.
--   * core.push_notifications: the outbox. A row is written in the same
--     transaction as the event it announces, and a background worker sends
--     it. One row per customer per competition announcement, so publishing
--     a competition again (after an edit) never notifies anyone twice.

CREATE TABLE IF NOT EXISTS customer.push_devices (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id   uuid NOT NULL REFERENCES customer.customers (id) ON DELETE CASCADE,
    token         text NOT NULL UNIQUE CHECK (char_length(token) BETWEEN 1 AND 4096),
    platform      text NOT NULL CHECK (platform IN ('android', 'ios')),
    app_version   text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS push_devices_customer_idx
    ON customer.push_devices (customer_id);

CREATE TABLE IF NOT EXISTS core.push_notifications (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id     uuid NOT NULL REFERENCES customer.customers (id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('competition_announced')),
    competition_id  uuid REFERENCES competition.competitions (id) ON DELETE CASCADE,
    title           text NOT NULL,
    body            text NOT NULL,
    data            jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- pending: waiting to send; sent: reached at least one device;
    -- skipped: nothing to send to, or no longer relevant; failed: gave up.
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'sent', 'skipped', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    last_error      text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS push_notifications_once_uq
    ON core.push_notifications (kind, competition_id, customer_id);

CREATE INDEX IF NOT EXISTS push_notifications_pending_idx
    ON core.push_notifications (next_attempt_at)
    WHERE status = 'pending';
