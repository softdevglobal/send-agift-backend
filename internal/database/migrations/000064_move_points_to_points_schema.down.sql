-- Reverses 000064: the points tables go back to finance, data and all.

ALTER FUNCTION points.forbid_points_ledger_mutation() SET SCHEMA finance;

CREATE OR REPLACE FUNCTION finance.forbid_points_ledger_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'finance.points_ledger is append-only; post a compensating entry instead';
END;
$$;

ALTER TABLE IF EXISTS points.points_purchases       SET SCHEMA finance;
ALTER TABLE IF EXISTS points.seller_points_accounts SET SCHEMA finance;
ALTER TABLE IF EXISTS points.points_earning_rules   SET SCHEMA finance;
ALTER TABLE IF EXISTS points.points_ledger          SET SCHEMA finance;
ALTER TABLE IF EXISTS points.points_accounts        SET SCHEMA finance;
