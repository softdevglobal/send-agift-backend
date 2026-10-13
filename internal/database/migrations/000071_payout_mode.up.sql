-- How a seller wants delivered orders paid out: 'automatic' sends each line
-- to their Stripe account as soon as it is delivered, 'manual' keeps them as
-- an available balance until the seller withdraws it.
ALTER TABLE seller.seller_payout_accounts
    ADD COLUMN IF NOT EXISTS payout_mode text NOT NULL DEFAULT 'automatic';

ALTER TABLE seller.seller_payout_accounts
    DROP CONSTRAINT IF EXISTS seller_payout_accounts_payout_mode_check;
ALTER TABLE seller.seller_payout_accounts
    ADD CONSTRAINT seller_payout_accounts_payout_mode_check
    CHECK (payout_mode IN ('automatic', 'manual'));
