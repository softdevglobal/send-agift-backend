-- The member bonus is credited to the customer's points (My points), the
-- same balance as every other point they earn, not to a separate
-- competition points lot.
--
--   member_bonus           + the period's bonus, when its payment goes through
--   member_bonus_reversal  - taken back when that payment is refunded

ALTER TABLE points.points_ledger
    DROP CONSTRAINT IF EXISTS points_ledger_entry_type_check,
    ADD CONSTRAINT points_ledger_entry_type_check CHECK (entry_type IN (
        'admin_grant', 'admin_deduction', 'play_debit', 'play_refund', 'correction',
        'order_reward', 'order_reversal', 'signup_bonus',
        'product_reward', 'product_reward_reversal',
        'gift_points_sent', 'gift_points_received', 'gift_points_returned', 'gift_points_reversal',
        'prize_points',
        'points_purchase', 'reward_funding', 'reward_funding_return',
        'member_bonus', 'member_bonus_reversal')),
    DROP CONSTRAINT IF EXISTS points_ledger_sign,
    ADD CONSTRAINT points_ledger_sign CHECK (
        (entry_type IN ('play_debit', 'admin_deduction', 'order_reversal', 'product_reward_reversal',
                        'gift_points_sent', 'gift_points_reversal', 'reward_funding', 'member_bonus_reversal')
            AND amount_delta < 0) OR
        (entry_type IN ('play_refund', 'admin_grant', 'order_reward', 'signup_bonus', 'product_reward',
                        'gift_points_received', 'gift_points_returned', 'prize_points',
                        'points_purchase', 'reward_funding_return', 'member_bonus')
            AND amount_delta > 0) OR
        entry_type = 'correction');

-- Move member bonuses already credited as competition points into My points:
-- the unused units leave the lot (balanced postings), and the customer's
-- points account gains them with a ledger row.
DO $$
DECLARE
    l   record;
    tx  uuid;
    bal bigint;
BEGIN
    FOR l IN
        SELECT lot.id, lot.customer_id, lot.source_id AS slot_id, lot.available
        FROM points.cp_lots lot
        WHERE lot.source = 'member_bonus' AND lot.available > 0
        FOR UPDATE
    LOOP
        INSERT INTO points.cp_transactions (operation_key, kind, customer_id, actor_type, reason)
        VALUES ('cp:to-points:' || l.id, 'reverse', l.customer_id, 'system', 'member bonus moved to My points')
        RETURNING id INTO tx;
        INSERT INTO points.cp_postings (transaction_id, lot_id, bucket, delta)
        VALUES (tx, l.id, 'available', -l.available), (tx, l.id, 'reversed', l.available);
        UPDATE points.cp_lots
        SET available = 0, reversed = reversed + l.available, updated_at = now()
        WHERE id = l.id;

        INSERT INTO points.points_accounts (customer_id) VALUES (l.customer_id)
        ON CONFLICT (customer_id) DO NOTHING;
        UPDATE points.points_accounts
        SET balance = balance + l.available, lifetime_earned = lifetime_earned + l.available, updated_at = now()
        WHERE customer_id = l.customer_id
        RETURNING balance INTO bal;
        INSERT INTO points.points_ledger
            (customer_id, entry_type, amount_delta, balance_after, idempotency_key, reason, actor_type,
             reference_type, reference_id)
        VALUES (l.customer_id, 'member_bonus', l.available, bal, 'member-bonus:' || l.slot_id,
                'Membership bonus', 'system', 'membership_bonus_slot', l.slot_id);
    END LOOP;
END;
$$;
