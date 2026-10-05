DELETE FROM core.push_notifications WHERE kind = 'competition_overtaken';

DROP INDEX IF EXISTS core.push_notifications_once_uq;
CREATE UNIQUE INDEX IF NOT EXISTS push_notifications_once_uq
    ON core.push_notifications (kind, competition_id, customer_id);

ALTER TABLE core.push_notifications
    DROP CONSTRAINT IF EXISTS push_notifications_kind_check,
    ADD CONSTRAINT push_notifications_kind_check CHECK (kind IN ('competition_announced'));

UPDATE competition.competitions SET max_attempts_per_customer = 100 WHERE max_attempts_per_customer = 0;
ALTER TABLE competition.competitions
    ALTER COLUMN max_attempts_per_customer DROP DEFAULT,
    DROP CONSTRAINT IF EXISTS competitions_max_attempts_per_customer_check,
    ADD CONSTRAINT competitions_max_attempts_per_customer_check
        CHECK (max_attempts_per_customer BETWEEN 1 AND 10000);
