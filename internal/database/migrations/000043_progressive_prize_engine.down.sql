-- Reverses 000043. The ledgers are dropped outright: only run this on a
-- database where losing points and prize history is acceptable.
DROP TABLE IF EXISTS competition.reconciliation_runs;
DROP TABLE IF EXISTS competition.competition_views;
DROP TABLE IF EXISTS competition.play_rejections;
DROP TABLE IF EXISTS core.outbox_events;

DROP TABLE IF EXISTS competition.prize_ledger;
DROP FUNCTION IF EXISTS competition.forbid_prize_ledger_mutation();

ALTER TABLE competition.competition_winners
    DROP COLUMN IF EXISTS prize_value_cents,
    DROP COLUMN IF EXISTS settlement_status,
    DROP COLUMN IF EXISTS settlement_reference,
    DROP COLUMN IF EXISTS settled_at;

ALTER TABLE competition.competition_attempts
    DROP CONSTRAINT IF EXISTS competition_attempts_points_ledger_fk;
ALTER TABLE finance.points_ledger
    DROP CONSTRAINT IF EXISTS points_ledger_attempt_fk;

DROP INDEX IF EXISTS competition.competition_attempts_client_request_uq;
DROP INDEX IF EXISTS competition.competition_attempts_daily_idx;
ALTER TABLE competition.competition_attempts
    DROP COLUMN IF EXISTS client_request_id,
    DROP COLUMN IF EXISTS prize_increment_cents,
    DROP COLUMN IF EXISTS prize_before_cents,
    DROP COLUMN IF EXISTS prize_after_cents,
    DROP COLUMN IF EXISTS risk_metadata,
    DROP COLUMN IF EXISTS refunded_at;

-- Paused rounds and large play limits cannot survive the old checks.
UPDATE competition.competitions SET status = 'live' WHERE status = 'paused';
UPDATE competition.competitions SET max_attempts_per_customer = 100 WHERE max_attempts_per_customer > 100;

ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_prize_cap,
    DROP COLUMN IF EXISTS prize_growth_enabled,
    DROP COLUMN IF EXISTS prize_type,
    DROP COLUMN IF EXISTS winner_method,
    DROP COLUMN IF EXISTS start_prize_cents,
    DROP COLUMN IF EXISTS increment_per_play_cents,
    DROP COLUMN IF EXISTS max_prize_cents,
    DROP COLUMN IF EXISTS continue_at_cap,
    DROP COLUMN IF EXISTS daily_play_limit,
    DROP COLUMN IF EXISTS min_plays_to_win,
    DROP COLUMN IF EXISTS current_prize_cents,
    DROP COLUMN IF EXISTS eligible_play_count,
    DROP COLUMN IF EXISTS unique_player_count,
    DROP COLUMN IF EXISTS prize_version,
    DROP COLUMN IF EXISTS final_prize_cents,
    DROP COLUMN IF EXISTS round_no,
    DROP COLUMN IF EXISTS previous_round_id,
    DROP COLUMN IF EXISTS config_version,
    DROP COLUMN IF EXISTS paused_at,
    DROP COLUMN IF EXISTS closed_at,
    DROP COLUMN IF EXISTS updated_by_admin_id;

ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_status_check,
    ADD CONSTRAINT competitions_status_check CHECK (status IN (
        'draft', 'scheduled', 'live', 'closed', 'frozen', 'finalised', 'cancelled')),
    DROP CONSTRAINT IF EXISTS competitions_max_attempts_per_customer_check,
    ADD CONSTRAINT competitions_max_attempts_per_customer_check
        CHECK (max_attempts_per_customer BETWEEN 1 AND 100);

DROP TABLE IF EXISTS finance.points_ledger;
DROP FUNCTION IF EXISTS finance.forbid_points_ledger_mutation();
DROP TABLE IF EXISTS finance.points_accounts;

ALTER TABLE core.country_capabilities
    DROP COLUMN IF EXISTS progressive_prizes_enabled;
