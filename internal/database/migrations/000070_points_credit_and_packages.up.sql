-- Seller points: rewards on credit, settled from the next purchase, and
-- point packages a Super Admin manages.
--
-- Until now a product's reward was only promised while the seller held
-- enough points, so a seller who ran low quietly stopped rewarding
-- customers. Now the customer always gets the reward and, when the seller
-- is short, the platform fronts the points: the seller's balance may go
-- below what they have promised, down to their credit limit. Whatever is
-- owed comes out of their next points purchase first.

-- ─── Seller credit ────────────────────────────────────────────────────────
ALTER TABLE points.seller_points_accounts
    ADD COLUMN IF NOT EXISTS credit_limit bigint NOT NULL DEFAULT 1000 CHECK (credit_limit >= 0);

-- A balance may go negative (points the platform fronted), and promises may
-- exceed it, but never by more than the seller's credit.
ALTER TABLE points.seller_points_accounts
    DROP CONSTRAINT IF EXISTS seller_points_accounts_balance_check,
    DROP CONSTRAINT IF EXISTS seller_points_reserved_within_balance,
    DROP CONSTRAINT IF EXISTS seller_points_within_credit,
    ADD CONSTRAINT seller_points_within_credit CHECK (balance - reserved >= -credit_limit);

-- A seller's ledger rows can show a negative balance; a customer's never.
ALTER TABLE points.points_ledger
    DROP CONSTRAINT IF EXISTS points_ledger_balance_after_check,
    ADD CONSTRAINT points_ledger_balance_after_check
        CHECK (balance_after >= 0 OR seller_id IS NOT NULL);

-- ─── Point packages ───────────────────────────────────────────────────────
-- Ready-made bundles a seller can buy, set up by a Super Admin. Bonus points
-- are added on top of the points the price pays for.
CREATE TABLE IF NOT EXISTS points.point_packages (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    description         text CHECK (description IS NULL OR char_length(description) <= 200),
    points              bigint NOT NULL CHECK (points > 0),
    bonus_points        bigint NOT NULL DEFAULT 0 CHECK (bonus_points >= 0),
    price_cents         bigint NOT NULL CHECK (price_cents > 0),
    currency            text NOT NULL,
    -- A short label shown on the card, e.g. "Most popular".
    badge               text CHECK (badge IS NULL OR char_length(badge) <= 24),
    is_active           boolean NOT NULL DEFAULT true,
    sort_order          integer NOT NULL DEFAULT 0,
    created_by_admin_id uuid,
    updated_by_admin_id uuid,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS point_packages_active_idx
    ON points.point_packages (is_active, sort_order, price_cents);

-- ─── Purchases: package bought, bonus, and debt settled ───────────────────
ALTER TABLE points.points_purchases
    ADD COLUMN IF NOT EXISTS package_id     uuid REFERENCES points.point_packages (id) ON DELETE RESTRICT,
    -- The package's name when bought, so a later rename does not rewrite history.
    ADD COLUMN IF NOT EXISTS package_name   text,
    -- Included in points; kept apart so the bonus can be shown.
    ADD COLUMN IF NOT EXISTS bonus_points   bigint NOT NULL DEFAULT 0 CHECK (bonus_points >= 0),
    -- How much of the credited points went to paying off owed points.
    ADD COLUMN IF NOT EXISTS points_settled bigint NOT NULL DEFAULT 0 CHECK (points_settled >= 0);
