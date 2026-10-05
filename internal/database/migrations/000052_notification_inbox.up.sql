-- The app's notification inbox reads the same rows the push sender sends,
-- so every announcement is in the inbox whether or not the push reached a
-- phone. read_at marks it seen there.
ALTER TABLE core.push_notifications
    ADD COLUMN IF NOT EXISTS read_at timestamptz;

CREATE INDEX IF NOT EXISTS push_notifications_inbox_idx
    ON core.push_notifications (customer_id, created_at DESC);
