-- Reverses 000044. Earned points and draws are dropped with it: only run
-- this where losing that history is acceptable.
ALTER TABLE finance.points_ledger DISABLE TRIGGER points_ledger_append_only;
DELETE FROM finance.points_ledger
WHERE entry_type IN ('order_reward', 'order_reversal', 'signup_bonus');
ALTER TABLE finance.points_ledger ENABLE TRIGGER points_ledger_append_only;

ALTER TABLE finance.points_ledger
    DROP CONSTRAINT IF EXISTS points_ledger_sign,
    ADD CONSTRAINT points_ledger_sign CHECK (
        (entry_type IN ('play_debit', 'admin_deduction') AND amount_delta < 0) OR
        (entry_type IN ('play_refund', 'admin_grant') AND amount_delta > 0) OR
        entry_type = 'correction'),
    DROP CONSTRAINT IF EXISTS points_ledger_entry_type_check,
    ADD CONSTRAINT points_ledger_entry_type_check CHECK (entry_type IN (
        'admin_grant', 'admin_deduction', 'play_debit', 'play_refund', 'correction')),
    DROP COLUMN IF EXISTS order_id;

DROP TABLE IF EXISTS finance.points_earning_rules;
DROP INDEX IF EXISTS competition.competition_attempts_device_idx;
DROP TABLE IF EXISTS competition.prize_draws;
DROP FUNCTION IF EXISTS competition.forbid_prize_draw_mutation();

ALTER TABLE competition.competition_winners
    DROP CONSTRAINT IF EXISTS competition_winners_has_source,
    DROP COLUMN IF EXISTS attempt_id;
ALTER TABLE competition.competition_attempts DROP COLUMN IF EXISTS result_payload;
ALTER TABLE competition.competitions DROP COLUMN IF EXISTS win_odds;

-- The chance games stay in the catalog, because rounds may point at them;
-- without this migration's code nothing can play them. Chance winners keep
-- their nullable score link.

ALTER TABLE core.country_capabilities DROP COLUMN IF EXISTS chance_games_enabled;
