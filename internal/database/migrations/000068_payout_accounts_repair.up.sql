-- seller.seller_payout_accounts can already exist in a database that was set
-- up from the design document before 000067, in which case 000067's
-- "create if not exists" left it as it was. Bring it to the shape the code
-- reads and writes. A no-op on a database 000067 created.

ALTER TABLE seller.seller_payout_accounts
    ADD COLUMN IF NOT EXISTS details_submitted boolean NOT NULL DEFAULT false;

-- One Stripe account per seller, and one seller per Stripe account.
CREATE UNIQUE INDEX IF NOT EXISTS seller_payout_accounts_seller_provider_uq
    ON seller.seller_payout_accounts (seller_id, provider);
CREATE UNIQUE INDEX IF NOT EXISTS seller_payout_accounts_provider_account_uq
    ON seller.seller_payout_accounts (provider, provider_account_id);
