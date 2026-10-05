-- Players may play a competition as often as their points allow: a play
-- limit of 0 now means no limit, and it is the default. Competitions that
-- have not finished move to it.
ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_max_attempts_per_customer_check,
    ADD CONSTRAINT competitions_max_attempts_per_customer_check
        CHECK (max_attempts_per_customer BETWEEN 0 AND 10000),
    ALTER COLUMN max_attempts_per_customer SET DEFAULT 0;

UPDATE competition.competitions
SET max_attempts_per_customer = 0, updated_at = now()
WHERE status IN ('draft', 'scheduled', 'live', 'paused');

-- A second kind of notification: someone just beat your score. A player
-- can be beaten many times in one competition, so only the announcement is
-- limited to one per player.
ALTER TABLE core.push_notifications
    DROP CONSTRAINT IF EXISTS push_notifications_kind_check,
    ADD CONSTRAINT push_notifications_kind_check
        CHECK (kind IN ('competition_announced', 'competition_overtaken'));

DROP INDEX IF EXISTS core.push_notifications_once_uq;
CREATE UNIQUE INDEX IF NOT EXISTS push_notifications_once_uq
    ON core.push_notifications (kind, competition_id, customer_id)
    WHERE kind = 'competition_announced';
