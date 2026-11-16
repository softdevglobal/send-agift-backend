-- Puts the points system back where it was. Game entries, advertising
-- packages and customers' states are dropped.

DROP TABLE IF EXISTS competition.competition_states;
ALTER TABLE competition.competition_attempts DROP COLUMN IF EXISTS entries_spent, DROP COLUMN IF EXISTS entry_ledger_id;
ALTER TABLE competition.competitions DROP COLUMN IF EXISTS entries_per_attempt;
ALTER TABLE competition.games DROP COLUMN IF EXISTS entry_cost;

DROP INDEX IF EXISTS marketplace.order_items_entries_open_idx;
ALTER TABLE marketplace.order_items
    DROP CONSTRAINT IF EXISTS order_items_entries_has,
    DROP COLUMN IF EXISTS game_entries,
    DROP COLUMN IF EXISTS entries_status,
    DROP COLUMN IF EXISTS promotion_id;

DROP SCHEMA IF EXISTS ads CASCADE;
DROP SCHEMA IF EXISTS entries CASCADE;

ALTER TABLE customer.customers
    DROP COLUMN IF EXISTS state_id, DROP COLUMN IF EXISTS state_set_at, DROP COLUMN IF EXISTS state_source;
DROP TABLE IF EXISTS core.states;

ALTER TABLE membership.plan_offers ALTER COLUMN member_bonus_points DROP DEFAULT;

ALTER TABLE core.country_capabilities RENAME COLUMN entries_usage_enabled TO points_usage_enabled;
ALTER TABLE core.country_capabilities RENAME COLUMN entries_earning_enabled TO points_earning_enabled;
ALTER TABLE finance.refunds RENAME COLUMN entries_reversal_status TO points_reversal_status;
ALTER TABLE competition.games RENAME COLUMN legacy_play_cost_points TO play_cost_points;
ALTER TABLE competition.competition_attempts RENAME COLUMN legacy_points_ledger_id TO points_ledger_id;
ALTER TABLE competition.competition_attempts RENAME COLUMN legacy_points_spent TO points_spent;
ALTER TABLE competition.competitions RENAME COLUMN legacy_cp_allowed_sources TO cp_allowed_sources;
ALTER TABLE competition.competitions RENAME COLUMN legacy_points_unit TO points_unit;
ALTER TABLE competition.competitions RENAME COLUMN legacy_prize_points TO prize_points;
ALTER TABLE competition.competitions RENAME COLUMN legacy_points_per_attempt TO points_per_attempt;
ALTER TABLE seller.products RENAME COLUMN legacy_points_display_enabled TO points_display_enabled;
ALTER TABLE seller.products RENAME COLUMN legacy_reward_points TO reward_points;
ALTER TABLE marketplace.orders RENAME COLUMN legacy_gift_points_recipient_id TO gift_points_recipient_id;
ALTER TABLE marketplace.orders RENAME COLUMN legacy_gift_points_status TO gift_points_status;
ALTER TABLE marketplace.orders RENAME COLUMN legacy_gift_points TO gift_points;
ALTER TABLE marketplace.order_items RENAME COLUMN legacy_reward_status TO reward_status;
ALTER TABLE marketplace.order_items RENAME COLUMN legacy_reward_points_per_unit TO reward_points_per_unit;
ALTER TABLE marketplace.order_items RENAME COLUMN legacy_reward_points TO reward_points;

DROP TRIGGER IF EXISTS points_accounts_frozen ON legacy_points.points_accounts;
DROP TRIGGER IF EXISTS seller_points_accounts_frozen ON legacy_points.seller_points_accounts;
DROP FUNCTION IF EXISTS legacy_points.forbid_balance_change();

ALTER TABLE legacy_points.bonus_slots SET SCHEMA membership;
ALTER TABLE legacy_points.cp_entry_lots SET SCHEMA competition;
ALTER TABLE legacy_points.cp_entries SET SCHEMA competition;
ALTER SCHEMA legacy_points RENAME TO points;
