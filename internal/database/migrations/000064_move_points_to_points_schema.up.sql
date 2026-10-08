-- Points live in their own schema.
--
-- The Database Structure doc (§3, §10) gives points the `points` schema —
-- "points accounts and immutable points ledger" — and keeps `finance` for
-- payments, refunds, payouts, fees and prize reserves. The points tables were
-- created in `finance` by 000043, 000044 and 000047; this moves every one of
-- them, with its data, into `points`.
--
-- ALTER TABLE ... SET SCHEMA moves a table with its rows, indexes,
-- constraints, identity sequence and triggers, and every foreign key to or
-- from it keeps working (they point at the table, not its name). Nothing is
-- copied or rewritten. finance.prize_reserves stays in finance: it is money
-- held for a prize, not points.

CREATE SCHEMA IF NOT EXISTS points;

ALTER TABLE IF EXISTS finance.points_accounts        SET SCHEMA points;
ALTER TABLE IF EXISTS finance.points_ledger          SET SCHEMA points;
ALTER TABLE IF EXISTS finance.points_earning_rules   SET SCHEMA points;
ALTER TABLE IF EXISTS finance.seller_points_accounts SET SCHEMA points;
ALTER TABLE IF EXISTS finance.points_purchases       SET SCHEMA points;

-- The function that keeps the ledger append-only moves with it, and now
-- names the ledger where it lives.
ALTER FUNCTION finance.forbid_points_ledger_mutation() SET SCHEMA points;

CREATE OR REPLACE FUNCTION points.forbid_points_ledger_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'points.points_ledger is append-only; post a compensating entry instead';
END;
$$;
