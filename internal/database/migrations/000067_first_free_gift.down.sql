-- 'confirmed' only exists for free gifts. Orders still sitting in it cannot
-- keep a status the old CHECK refuses, so they are closed.
UPDATE marketplace.orders SET status = 'cancelled', updated_at = now()
WHERE status = 'confirmed';

ALTER TABLE marketplace.orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE marketplace.orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('draft', 'pending_payment', 'paid', 'accepted',
                      'preparing', 'dispatched', 'delivered', 'cancelled', 'refunded'));

DROP TABLE IF EXISTS promo.free_gift_claims;
DROP TABLE IF EXISTS promo.free_gift_products;
DROP TABLE IF EXISTS promo.free_gift_programs;

DROP INDEX IF EXISTS marketplace.orders_customer_idempotency_uq;
ALTER TABLE marketplace.orders
    DROP CONSTRAINT IF EXISTS orders_free_gift_kind,
    DROP COLUMN IF EXISTS idempotency_key,
    DROP COLUMN IF EXISTS free_gift_budget_status,
    DROP COLUMN IF EXISTS free_gift_cost,
    DROP COLUMN IF EXISTS order_kind;

DROP SCHEMA IF EXISTS promo;
