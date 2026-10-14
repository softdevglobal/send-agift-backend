DROP INDEX IF EXISTS marketplace.orders_dispatched_notice_due_idx;
DROP INDEX IF EXISTS marketplace.orders_accepted_notice_due_idx;

ALTER TABLE marketplace.orders
    DROP COLUMN IF EXISTS dispatched_notified_at,
    DROP COLUMN IF EXISTS accepted_notified_at;

ALTER TABLE seller.sellers
    DROP COLUMN IF EXISTS free_gift_terms_accepted_at;
