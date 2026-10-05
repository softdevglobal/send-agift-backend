DROP INDEX IF EXISTS core.push_notifications_inbox_idx;
ALTER TABLE core.push_notifications DROP COLUMN IF EXISTS read_at;
