-- First free gift program.
--
--   * promo.free_gift_programs: one seller's marketing budget and on/off state.
--   * promo.free_gift_products: the products that seller offers as a free gift.
--   * promo.free_gift_claims:   one used one-time right (a sender or a recipient).
--   * marketplace.orders:       the order kind, what the seller funded, safe
--                               retries, and the 'confirmed' status.

CREATE SCHEMA IF NOT EXISTS promo;

-- One seller's marketing budget for free gifts.
CREATE TABLE IF NOT EXISTS promo.free_gift_programs (
    seller_id     uuid PRIMARY KEY REFERENCES seller.sellers (id) ON DELETE CASCADE,
    status        text NOT NULL DEFAULT 'paused'
                  CHECK (status IN ('active', 'paused', 'stopped')),  -- stopped = by an admin
    currency      text NOT NULL,
    budget_cap    bigint NOT NULL CHECK (budget_cap > 0),             -- minor units
    budget_held   bigint NOT NULL DEFAULT 0 CHECK (budget_held >= 0),
    budget_spent  bigint NOT NULL DEFAULT 0 CHECK (budget_spent >= 0),
    terms_version text,
    accepted_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT free_gift_budget_within_cap CHECK (budget_held + budget_spent <= budget_cap)
);

-- The products a seller offers as a free gift.
CREATE TABLE IF NOT EXISTS promo.free_gift_products (
    product_id  uuid PRIMARY KEY REFERENCES seller.products (id) ON DELETE CASCADE,
    seller_id   uuid NOT NULL REFERENCES promo.free_gift_programs (seller_id) ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    gifts_cap   integer CHECK (gifts_cap > 0),                        -- null = budget only
    gifts_count integer NOT NULL DEFAULT 0 CHECK (gifts_count >= 0),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT free_gift_products_within_cap CHECK (gifts_cap IS NULL OR gifts_count <= gifts_cap)
);

CREATE INDEX IF NOT EXISTS free_gift_products_seller_idx
    ON promo.free_gift_products (seller_id) WHERE status = 'active';

-- One-time rights. The unique index is the rule.
CREATE TABLE IF NOT EXISTS promo.free_gift_claims (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    role         text NOT NULL CHECK (role IN ('send', 'receive')),
    identity_key text NOT NULL,          -- customer:<id> | phone:<hmac> | email:<hmac>
    key_version  integer NOT NULL DEFAULT 1,
    customer_id  uuid REFERENCES customer.customers (id) ON DELETE SET NULL,
    order_id     uuid REFERENCES marketplace.orders (id),
    state        text NOT NULL DEFAULT 'used' CHECK (state IN ('used', 'released')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    released_at  timestamptz
);

-- Released rows stop counting, so a cancellation before dispatch gives the
-- right back.
CREATE UNIQUE INDEX IF NOT EXISTS free_gift_claims_one_uq
    ON promo.free_gift_claims (role, identity_key) WHERE state = 'used';

CREATE INDEX IF NOT EXISTS free_gift_claims_order_idx
    ON promo.free_gift_claims (order_id);

-- Orders: the kind, what the seller funded, and safe retries.
ALTER TABLE marketplace.orders
    ADD COLUMN IF NOT EXISTS order_kind text NOT NULL DEFAULT 'paid'
        CHECK (order_kind IN ('paid', 'free_gift')),
    ADD COLUMN IF NOT EXISTS free_gift_cost integer NOT NULL DEFAULT 0
        CHECK (free_gift_cost >= 0),
    ADD COLUMN IF NOT EXISTS free_gift_budget_status text NOT NULL DEFAULT 'none'
        CHECK (free_gift_budget_status IN ('none', 'held', 'spent', 'released')),
    ADD COLUMN IF NOT EXISTS idempotency_key text;

ALTER TABLE marketplace.orders
    DROP CONSTRAINT IF EXISTS orders_free_gift_kind,
    ADD CONSTRAINT orders_free_gift_kind
        CHECK ((order_kind = 'free_gift') = (free_gift_budget_status <> 'none'));

CREATE UNIQUE INDEX IF NOT EXISTS orders_customer_idempotency_uq
    ON marketplace.orders (customer_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- A free gift has nothing to pay, so it never sits in pending_payment.
-- The status CHECK from migration 000012 was written inline and unnamed, so
-- it is found by what it checks rather than by the name Postgres gave it.
DO $$
DECLARE
    c text;
BEGIN
    FOR c IN
        SELECT conname FROM pg_constraint
        WHERE conrelid = 'marketplace.orders'::regclass
          AND contype = 'c'
          AND pg_get_constraintdef(oid) LIKE '%pending_payment%'
    LOOP
        EXECUTE format('ALTER TABLE marketplace.orders DROP CONSTRAINT %I', c);
    END LOOP;
END $$;

ALTER TABLE marketplace.orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('draft', 'pending_payment', 'paid', 'confirmed', 'accepted',
                      'preparing', 'dispatched', 'delivered', 'cancelled', 'refunded'));
