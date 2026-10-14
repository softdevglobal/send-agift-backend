-- Free gift program: the admin sets each seller's marketing budget, and the
-- customer is emailed as their free gift moves along.
--
--   * seller.sellers.free_gift_terms_accepted_at: the seller ticked, at
--     registration, that they accept the marketing budget SendAGift sets.
--   * marketplace.orders.accepted_notified_at / dispatched_notified_at: the
--     customer has been told the seller accepted, or that delivery started.

ALTER TABLE seller.sellers
    ADD COLUMN IF NOT EXISTS free_gift_terms_accepted_at timestamptz;

-- A seller who already accepted the program terms by setting a budget has
-- agreed to the same thing.
UPDATE seller.sellers s
SET free_gift_terms_accepted_at = pg.accepted_at
FROM promo.free_gift_programs pg
WHERE pg.seller_id = s.id
  AND pg.accepted_at IS NOT NULL
  AND s.free_gift_terms_accepted_at IS NULL;

ALTER TABLE marketplace.orders
    ADD COLUMN IF NOT EXISTS accepted_notified_at timestamptz,
    ADD COLUMN IF NOT EXISTS dispatched_notified_at timestamptz;

-- Orders already past these steps are not announced after the fact.
UPDATE marketplace.orders
SET accepted_notified_at = now()
WHERE status IN ('accepted', 'preparing', 'dispatched', 'delivered', 'cancelled', 'refunded')
  AND accepted_notified_at IS NULL;

UPDATE marketplace.orders
SET dispatched_notified_at = now()
WHERE status IN ('dispatched', 'delivered', 'cancelled', 'refunded')
  AND dispatched_notified_at IS NULL;

CREATE INDEX IF NOT EXISTS orders_accepted_notice_due_idx
    ON marketplace.orders (updated_at)
    WHERE accepted_notified_at IS NULL AND status IN ('accepted', 'preparing', 'dispatched', 'delivered');

CREATE INDEX IF NOT EXISTS orders_dispatched_notice_due_idx
    ON marketplace.orders (updated_at)
    WHERE dispatched_notified_at IS NULL AND status IN ('dispatched', 'delivered');
