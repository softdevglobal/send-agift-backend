-- Customers can clear notifications from the app inbox, one by one or all at
-- once. The row is hidden rather than deleted because it is also the push
-- queue and the record that stops an announcement being sent twice.
ALTER TABLE core.push_notifications
    ADD COLUMN IF NOT EXISTS dismissed_at timestamptz;
