-- One row per shop on an order: the delivery the customer chose and paid for
-- that shop's parcel at checkout. orders.delivery_amount is the sum of these.
-- A shop with no row was not priced at checkout; the seller arranges it later.
CREATE TABLE IF NOT EXISTS marketplace.order_shop_deliveries (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id       uuid NOT NULL REFERENCES marketplace.orders (id) ON DELETE CASCADE,
    shop_id        uuid NOT NULL REFERENCES seller.shops (id),
    seller_id      uuid NOT NULL REFERENCES seller.sellers (id),
    mode           text NOT NULL CHECK (mode IN ('courier', 'seller_delivery')),
    provider       text NOT NULL DEFAULT '',
    service_name   text NOT NULL DEFAULT '',
    amount         integer NOT NULL CHECK (amount >= 0),
    currency       text NOT NULL,
    estimated_days integer CHECK (estimated_days IS NULL OR estimated_days >= 0),
    distance_km    numeric CHECK (distance_km IS NULL OR distance_km >= 0),
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, shop_id)
);

CREATE INDEX IF NOT EXISTS idx_order_shop_deliveries_seller
    ON marketplace.order_shop_deliveries (seller_id);

-- Backfill from checkout choices already saved on pending shipments.
INSERT INTO marketplace.order_shop_deliveries (
    order_id, shop_id, seller_id, mode, provider, service_name, amount, currency,
    estimated_days, distance_km
)
SELECT DISTINCT ON (oi.order_id, oi.shop_id)
    oi.order_id,
    oi.shop_id,
    oi.seller_id,
    CASE WHEN sh.delivery_mode = 'seller_managed' THEN 'seller_delivery' ELSE 'courier' END,
    CASE WHEN sh.delivery_mode = 'seller_managed' THEN 'Seller delivery'
         ELSE coalesce(sh.provider_metadata->'checkout_quote'->>'provider', sh.provider_metadata->>'provider', '') END,
    CASE WHEN sh.delivery_mode = 'seller_managed' THEN ''
         ELSE coalesce(sh.provider_metadata->'checkout_quote'->>'service_name', sh.provider_metadata->>'service_name', '') END,
    CASE WHEN sh.delivery_mode = 'seller_managed' THEN sh.price_amount
         ELSE coalesce(sh.provider_metadata->'checkout_quote'->>'amount', sh.provider_metadata->>'amount')::integer END,
    coalesce(
        CASE WHEN sh.delivery_mode = 'seller_managed' THEN sh.currency END,
        nullif(sh.provider_metadata->'checkout_quote'->>'currency', ''),
        nullif(sh.provider_metadata->>'currency', ''),
        o.currency
    ),
    CASE WHEN sh.delivery_mode = 'seller_managed' THEN sh.estimated_days END,
    CASE WHEN sh.delivery_mode = 'seller_managed' THEN sh.distance_km END
FROM marketplace.shipments sh
JOIN marketplace.order_items oi ON oi.id = sh.order_item_id
JOIN marketplace.orders o ON o.id = oi.order_id
WHERE sh.status = 'pending'
  AND (
        (sh.delivery_mode = 'seller_managed' AND sh.price_amount IS NOT NULL)
     OR (sh.provider_metadata->'checkout_quote'->>'amount') ~ '^[0-9]+$'
     OR (sh.provider_metadata->>'source' = 'checkout_quote' AND (sh.provider_metadata->>'amount') ~ '^[0-9]+$')
  )
ORDER BY oi.order_id, oi.shop_id, sh.created_at
ON CONFLICT (order_id, shop_id) DO NOTHING;
