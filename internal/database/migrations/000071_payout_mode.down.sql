ALTER TABLE seller.seller_payout_accounts
    DROP CONSTRAINT IF EXISTS seller_payout_accounts_payout_mode_check;
ALTER TABLE seller.seller_payout_accounts
    DROP COLUMN IF EXISTS payout_mode;
