-- The accepted and dispatched emails now follow the order's lines rather than
-- the order's status (a free gift with no delivery to pay stays "confirmed"
-- while its line moves). Orders whose lines were already past those steps
-- before this release are marked told, so they are not announced late.
UPDATE marketplace.orders o
SET accepted_notified_at = coalesce(accepted_notified_at, now())
WHERE accepted_notified_at IS NULL
  AND EXISTS (SELECT 1 FROM marketplace.order_items oi
              WHERE oi.order_id = o.id
                AND oi.fulfilment_status IN ('accepted', 'preparing', 'ready', 'dispatched', 'delivered'));

UPDATE marketplace.orders o
SET dispatched_notified_at = coalesce(dispatched_notified_at, now())
WHERE dispatched_notified_at IS NULL
  AND EXISTS (SELECT 1 FROM marketplace.order_items oi
              WHERE oi.order_id = o.id
                AND oi.fulfilment_status IN ('dispatched', 'delivered'));
