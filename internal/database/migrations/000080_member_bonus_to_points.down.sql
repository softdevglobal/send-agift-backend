-- Points already credited stay where they are; only new rows of these
-- types are refused again.
ALTER TABLE points.points_ledger
    DROP CONSTRAINT IF EXISTS points_ledger_entry_type_check,
    ADD CONSTRAINT points_ledger_entry_type_check CHECK (entry_type IN (
        'admin_grant', 'admin_deduction', 'play_debit', 'play_refund', 'correction',
        'order_reward', 'order_reversal', 'signup_bonus',
        'product_reward', 'product_reward_reversal',
        'gift_points_sent', 'gift_points_received', 'gift_points_returned', 'gift_points_reversal',
        'prize_points',
        'points_purchase', 'reward_funding', 'reward_funding_return')) NOT VALID,
    DROP CONSTRAINT IF EXISTS points_ledger_sign,
    ADD CONSTRAINT points_ledger_sign CHECK (
        (entry_type IN ('play_debit', 'admin_deduction', 'order_reversal', 'product_reward_reversal',
                        'gift_points_sent', 'gift_points_reversal', 'reward_funding')
            AND amount_delta < 0) OR
        (entry_type IN ('play_refund', 'admin_grant', 'order_reward', 'signup_bonus', 'product_reward',
                        'gift_points_received', 'gift_points_returned', 'prize_points',
                        'points_purchase', 'reward_funding_return')
            AND amount_delta > 0) OR
        entry_type = 'correction') NOT VALID;
