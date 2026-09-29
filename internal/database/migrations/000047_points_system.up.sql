-- Customer points system: sellers buy points, fund product rewards with them,
-- customers earn them from purchases, gifts and prizes, and spend them on
-- games.
--
-- The rules this schema enforces:
--   * One ledger for everyone. finance.points_ledger now holds sellers' rows
--     as well as customers', exactly one holder per row. It stays
--     append-only and the source of truth; the balances on the account rows
--     are caches written in the same transaction as the ledger row.
--   * A balance can never go negative, and a seller can never reserve more
--     than they hold.
--   * Points bought are only credited once the payment is confirmed, and at
--     the rate the purchase was quoted at (the rate itself is configuration).
--   * A product's reward points are reserved from the seller when the order
--     is placed, paid to the customer when the line is delivered, and given
--     back to the seller if the line is cancelled — so a seller can never
--     promise points they have not bought.
--   * Points a customer attaches to a gift leave their balance when the
--     order is placed and reach the recipient's account on delivery, or go
--     back to the sender if it never gets there.

-- ─── Ledger: any holder, and the spec's audit fields ──────────────────────
ALTER TABLE finance.points_ledger
    ALTER COLUMN customer_id DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS seller_id uuid REFERENCES seller.sellers (id) ON DELETE RESTRICT,
    -- What the change was about, e.g. ('order_item', <id>) or
    -- ('points_purchase', <id>), for rows no dedicated column covers.
    ADD COLUMN IF NOT EXISTS reference_type text,
    ADD COLUMN IF NOT EXISTS reference_id uuid,
    -- Rows are only written once a change has happened, so every row is
    -- completed; pending purchases live on finance.points_purchases.
    ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'completed' CHECK (status = 'completed'),
    ADD COLUMN IF NOT EXISTS balance_before bigint GENERATED ALWAYS AS (balance_after - amount_delta) STORED,
    ADD COLUMN IF NOT EXISTS direction text GENERATED ALWAYS AS (
        CASE WHEN amount_delta > 0 THEN 'credit' ELSE 'debit' END) STORED;

ALTER TABLE finance.points_ledger
    DROP CONSTRAINT IF EXISTS points_ledger_one_holder,
    ADD CONSTRAINT points_ledger_one_holder CHECK ((customer_id IS NULL) <> (seller_id IS NULL)),
    DROP CONSTRAINT IF EXISTS points_ledger_entry_type_check,
    ADD CONSTRAINT points_ledger_entry_type_check CHECK (entry_type IN (
        'admin_grant', 'admin_deduction', 'play_debit', 'play_refund', 'correction',
        'order_reward', 'order_reversal', 'signup_bonus',
        'product_reward', 'product_reward_reversal',
        'gift_points_sent', 'gift_points_received', 'gift_points_returned', 'gift_points_reversal',
        'prize_points',
        'points_purchase', 'reward_funding', 'reward_funding_return')),
    DROP CONSTRAINT IF EXISTS points_ledger_sign,
    ADD CONSTRAINT points_ledger_sign CHECK (
        (entry_type IN ('play_debit', 'admin_deduction', 'order_reversal', 'product_reward_reversal',
                        'gift_points_sent', 'gift_points_reversal', 'reward_funding')
            AND amount_delta < 0) OR
        (entry_type IN ('play_refund', 'admin_grant', 'order_reward', 'signup_bonus', 'product_reward',
                        'gift_points_received', 'gift_points_returned', 'prize_points',
                        'points_purchase', 'reward_funding_return')
            AND amount_delta > 0) OR
        entry_type = 'correction'),
    -- Sellers' rows are only ever these; everything else is a customer's.
    DROP CONSTRAINT IF EXISTS points_ledger_holder_type,
    ADD CONSTRAINT points_ledger_holder_type CHECK (
        entry_type IN ('admin_grant', 'admin_deduction', 'correction') OR
        (entry_type IN ('points_purchase', 'reward_funding', 'reward_funding_return')) = (seller_id IS NOT NULL));

CREATE INDEX IF NOT EXISTS points_ledger_seller_idx
    ON finance.points_ledger (seller_id, seq DESC)
    WHERE seller_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS points_ledger_reference_idx
    ON finance.points_ledger (reference_type, reference_id)
    WHERE reference_id IS NOT NULL;

-- ─── Seller accounts ──────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS finance.seller_points_accounts (
    seller_id           uuid PRIMARY KEY REFERENCES seller.sellers (id) ON DELETE RESTRICT,
    balance             bigint NOT NULL DEFAULT 0 CHECK (balance >= 0),
    -- Promised to customers on orders not yet delivered. Still the
    -- seller's, but not theirs to promise again.
    reserved            bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    lifetime_purchased  bigint NOT NULL DEFAULT 0 CHECK (lifetime_purchased >= 0),
    lifetime_spent      bigint NOT NULL DEFAULT 0 CHECK (lifetime_spent >= 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT seller_points_reserved_within_balance CHECK (reserved <= balance)
);

-- ─── Seller points purchases ──────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS finance.points_purchases (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id              uuid NOT NULL REFERENCES seller.sellers (id) ON DELETE RESTRICT,
    -- What the seller asked to pay, in minor units of currency.
    amount_cents           bigint NOT NULL CHECK (amount_cents > 0),
    currency               text NOT NULL DEFAULT 'USD',
    -- The rate the purchase was quoted at, kept so a later rate change never
    -- reprices a purchase already started.
    cents_per_point        integer NOT NULL CHECK (cents_per_point > 0),
    points                 bigint NOT NULL CHECK (points > 0),
    -- What the payment provider actually confirmed, and the points that paid
    -- for, set together on completion.
    paid_amount_cents      bigint CHECK (paid_amount_cents >= 0),
    points_credited        bigint CHECK (points_credited > 0),
    status                 text NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending', 'completed', 'failed', 'cancelled')),
    provider               text NOT NULL,
    provider_reference     text,
    checkout_url           text,
    failure_reason         text,
    idempotency_key        text NOT NULL,
    ledger_entry_id        uuid REFERENCES finance.points_ledger (id) ON DELETE RESTRICT,
    confirmed_by           text CHECK (confirmed_by IN ('provider', 'test', 'admin')),
    confirmed_by_admin_id  uuid,
    completed_at           timestamptz,
    failed_at              timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    UNIQUE (seller_id, idempotency_key),
    CONSTRAINT points_purchases_completed_credit CHECK (
        status <> 'completed' OR
        (paid_amount_cents IS NOT NULL AND points_credited IS NOT NULL AND ledger_entry_id IS NOT NULL
         AND completed_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS points_purchases_seller_idx
    ON finance.points_purchases (seller_id, created_at DESC);
CREATE INDEX IF NOT EXISTS points_purchases_status_idx
    ON finance.points_purchases (status, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS points_purchases_provider_ref_uq
    ON finance.points_purchases (provider, provider_reference)
    WHERE provider_reference IS NOT NULL;

-- ─── Product reward points ────────────────────────────────────────────────
ALTER TABLE seller.products
    ADD COLUMN IF NOT EXISTS reward_points integer NOT NULL DEFAULT 0
        CHECK (reward_points BETWEEN 0 AND 1000000);

-- Each line keeps the reward it was sold with, so editing the product later
-- never changes what an order already promised.
ALTER TABLE marketplace.order_items
    ADD COLUMN IF NOT EXISTS reward_points_per_unit integer NOT NULL DEFAULT 0
        CHECK (reward_points_per_unit >= 0),
    ADD COLUMN IF NOT EXISTS reward_points bigint NOT NULL DEFAULT 0 CHECK (reward_points >= 0),
    ADD COLUMN IF NOT EXISTS reward_status text NOT NULL DEFAULT 'none'
        CHECK (reward_status IN ('none', 'reserved', 'awarded', 'released', 'reversed'));

ALTER TABLE marketplace.order_items
    DROP CONSTRAINT IF EXISTS order_items_reward_has_points,
    ADD CONSTRAINT order_items_reward_has_points CHECK (reward_status = 'none' OR reward_points > 0);

CREATE INDEX IF NOT EXISTS order_items_reward_open_idx
    ON marketplace.order_items (reward_status)
    WHERE reward_status IN ('reserved', 'awarded');

-- ─── Points attached to a gift ────────────────────────────────────────────
ALTER TABLE marketplace.orders
    ADD COLUMN IF NOT EXISTS gift_points bigint NOT NULL DEFAULT 0 CHECK (gift_points >= 0),
    ADD COLUMN IF NOT EXISTS gift_points_status text NOT NULL DEFAULT 'none'
        CHECK (gift_points_status IN ('none', 'held', 'delivered', 'returned', 'reversed')),
    -- The customer account the points reached, matched on the recipient's
    -- email at delivery.
    ADD COLUMN IF NOT EXISTS gift_points_recipient_id uuid REFERENCES customer.customers (id) ON DELETE RESTRICT;

ALTER TABLE marketplace.orders
    DROP CONSTRAINT IF EXISTS orders_gift_points_status,
    ADD CONSTRAINT orders_gift_points_status CHECK ((gift_points = 0) = (gift_points_status = 'none'));

CREATE INDEX IF NOT EXISTS orders_gift_points_open_idx
    ON marketplace.orders (gift_points_status)
    WHERE gift_points_status IN ('held', 'delivered');

-- ─── Points as a competition prize ────────────────────────────────────────
ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_prize_type_check,
    ADD CONSTRAINT competitions_prize_type_check
        CHECK (prize_type IN ('cash', 'product', 'voucher', 'gift', 'points', 'other')),
    -- Points each validated winner receives.
    ADD COLUMN IF NOT EXISTS prize_points bigint CHECK (prize_points BETWEEN 1 AND 100000000);

ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_points_prize_amount,
    ADD CONSTRAINT competitions_points_prize_amount CHECK (prize_type <> 'points' OR prize_points IS NOT NULL);
