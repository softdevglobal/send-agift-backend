-- Distance bands for seller local delivery (e.g. within 5 km = 5000 cents).
-- price_amount 0 means free. Shop and recipient lat/lng are used later to pick a band.
CREATE TABLE IF NOT EXISTS seller.shop_delivery_zones (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    shop_id       uuid NOT NULL REFERENCES seller.shops (id) ON DELETE CASCADE,
    max_km        numeric NOT NULL CHECK (max_km > 0),
    price_amount  integer NOT NULL CHECK (price_amount >= 0),
    currency      text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_id, max_km)
);

CREATE INDEX IF NOT EXISTS idx_shop_delivery_zones_shop_id
    ON seller.shop_delivery_zones (shop_id, max_km);
