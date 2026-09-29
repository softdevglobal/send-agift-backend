-- Reverses 000047. Sellers' points, purchases and the new kinds of customer
-- points history are dropped with it: only run this where losing that
-- history is acceptable. Customer balances are not recomputed.
ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_points_prize_amount,
    DROP COLUMN IF EXISTS prize_points;
UPDATE competition.competitions SET prize_type = 'other' WHERE prize_type = 'points';
ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_prize_type_check,
    ADD CONSTRAINT competitions_prize_type_check
        CHECK (prize_type IN ('cash', 'product', 'voucher', 'gift', 'other'));

DROP INDEX IF EXISTS marketplace.orders_gift_points_open_idx;
ALTER TABLE marketplace.orders
    DROP CONSTRAINT IF EXISTS orders_gift_points_status,
    DROP COLUMN IF EXISTS gift_points_recipient_id,
    DROP COLUMN IF EXISTS gift_points_status,
    DROP COLUMN IF EXISTS gift_points;

DROP INDEX IF EXISTS marketplace.order_items_reward_open_idx;
ALTER TABLE marketplace.order_items
    DROP CONSTRAINT IF EXISTS order_items_reward_has_points,
    DROP COLUMN IF EXISTS reward_status,
    DROP COLUMN IF EXISTS reward_points,
    DROP COLUMN IF EXISTS reward_points_per_unit;

ALTER TABLE seller.products DROP COLUMN IF EXISTS reward_points;

DROP TABLE IF EXISTS finance.points_purchases;
DROP TABLE IF EXISTS finance.seller_points_accounts;

ALTER TABLE finance.points_ledger DISABLE TRIGGER points_ledger_append_only;
DELETE FROM finance.points_ledger
WHERE seller_id IS NOT NULL
   OR entry_type IN ('product_reward', 'product_reward_reversal', 'gift_points_sent',
                     'gift_points_received', 'gift_points_returned', 'gift_points_reversal',
                     'prize_points', 'points_purchase', 'reward_funding', 'reward_funding_return');
ALTER TABLE finance.points_ledger ENABLE TRIGGER points_ledger_append_only;

DROP INDEX IF EXISTS finance.points_ledger_reference_idx;
DROP INDEX IF EXISTS finance.points_ledger_seller_idx;
ALTER TABLE finance.points_ledger
    DROP CONSTRAINT IF EXISTS points_ledger_holder_type,
    DROP CONSTRAINT IF EXISTS points_ledger_one_holder,
    DROP CONSTRAINT IF EXISTS points_ledger_sign,
    ADD CONSTRAINT points_ledger_sign CHECK (
        (entry_type IN ('play_debit', 'admin_deduction', 'order_reversal') AND amount_delta < 0) OR
        (entry_type IN ('play_refund', 'admin_grant', 'order_reward', 'signup_bonus') AND amount_delta > 0) OR
        entry_type = 'correction'),
    DROP CONSTRAINT IF EXISTS points_ledger_entry_type_check,
    ADD CONSTRAINT points_ledger_entry_type_check CHECK (entry_type IN (
        'admin_grant', 'admin_deduction', 'play_debit', 'play_refund', 'correction',
        'order_reward', 'order_reversal', 'signup_bonus')),
    DROP COLUMN IF EXISTS direction,
    DROP COLUMN IF EXISTS balance_before,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS reference_id,
    DROP COLUMN IF EXISTS reference_type,
    DROP COLUMN IF EXISTS seller_id,
    ALTER COLUMN customer_id SET NOT NULL;
