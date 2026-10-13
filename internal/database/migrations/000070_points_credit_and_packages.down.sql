ALTER TABLE points.points_purchases
    DROP COLUMN IF EXISTS points_settled,
    DROP COLUMN IF EXISTS bonus_points,
    DROP COLUMN IF EXISTS package_name,
    DROP COLUMN IF EXISTS package_id;

DROP TABLE IF EXISTS points.point_packages;

-- NOT VALID: rows written while credit was allowed may break the old rules.
ALTER TABLE points.points_ledger
    DROP CONSTRAINT IF EXISTS points_ledger_balance_after_check,
    ADD CONSTRAINT points_ledger_balance_after_check CHECK (balance_after >= 0) NOT VALID;

ALTER TABLE points.seller_points_accounts
    DROP CONSTRAINT IF EXISTS seller_points_within_credit,
    ADD CONSTRAINT seller_points_accounts_balance_check CHECK (balance >= 0) NOT VALID,
    ADD CONSTRAINT seller_points_reserved_within_balance CHECK (reserved <= balance) NOT VALID,
    DROP COLUMN IF EXISTS credit_limit;
