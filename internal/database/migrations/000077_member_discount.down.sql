ALTER TABLE marketplace.orders
    DROP CONSTRAINT IF EXISTS orders_member_discount_within_subtotal,
    DROP COLUMN IF EXISTS member_paid_period_id,
    DROP COLUMN IF EXISTS member_discount_bps,
    DROP COLUMN IF EXISTS member_discount_amount;
ALTER TABLE marketplace.order_items DROP COLUMN IF EXISTS member_discount_amount;
ALTER TABLE membership.plan_offers DROP COLUMN IF EXISTS member_discount_bps;
