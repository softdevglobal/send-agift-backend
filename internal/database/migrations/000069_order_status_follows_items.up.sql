-- An order's status used to stay "paid" while the seller accepted, prepared
-- and dispatched its items. The status now follows the items (see
-- refreshOrderStatusForItem), so bring orders already in flight in line.
-- Only orders already paid for are touched, and only forward.

UPDATE marketplace.orders o
SET status = p.next, updated_at = now()
FROM (
    SELECT oi.order_id,
           CASE
               WHEN bool_or(oi.fulfilment_status IN ('dispatched', 'delivered')) THEN 'dispatched'
               WHEN bool_or(oi.fulfilment_status IN ('preparing', 'ready')) THEN 'preparing'
               WHEN bool_or(oi.fulfilment_status = 'accepted') THEN 'accepted'
           END AS next
    FROM marketplace.order_items oi
    WHERE oi.fulfilment_status <> 'cancelled'
    GROUP BY oi.order_id
) p
WHERE o.id = p.order_id
  AND p.next IS NOT NULL
  AND o.status IN ('paid', 'accepted', 'preparing', 'dispatched')
  AND array_position(ARRAY['paid', 'accepted', 'preparing', 'dispatched'], p.next)
    > array_position(ARRAY['paid', 'accepted', 'preparing', 'dispatched'], o.status);
