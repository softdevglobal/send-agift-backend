CREATE TABLE IF NOT EXISTS seller.products (
    id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    shop_id                    uuid NOT NULL REFERENCES seller.shops (id) ON DELETE CASCADE,
    name                       text NOT NULL,
    slug                       text NOT NULL,
    description                text,
    product_type               text NOT NULL DEFAULT 'gift',
    price_amount               integer NOT NULL CHECK (price_amount >= 0),
    currency                   text NOT NULL,
    status                     text NOT NULL DEFAULT 'draft'
                               CHECK (status IN ('draft', 'published', 'paused', 'rejected')),
    occasion_tags              text[] NOT NULL DEFAULT '{}',
    customer_type_visibility   text NOT NULL DEFAULT 'both'
                               CHECK (customer_type_visibility IN ('personal', 'corporate', 'both')),
    points_display_enabled     boolean NOT NULL DEFAULT false,
    prep_minutes               integer NOT NULL DEFAULT 0 CHECK (prep_minutes >= 0),
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now(),
    UNIQUE (shop_id, slug)
);

CREATE INDEX IF NOT EXISTS products_shop_status_idx
    ON seller.products (shop_id, status);

CREATE INDEX IF NOT EXISTS products_occasion_tags_gin
    ON seller.products USING gin (occasion_tags);

CREATE TABLE IF NOT EXISTS seller.inventory (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id           uuid NOT NULL UNIQUE REFERENCES seller.products (id) ON DELETE CASCADE,
    available_qty        integer NOT NULL DEFAULT 0 CHECK (available_qty >= 0),
    reserved_qty         integer NOT NULL DEFAULT 0 CHECK (reserved_qty >= 0),
    low_stock_threshold  integer NOT NULL DEFAULT 0 CHECK (low_stock_threshold >= 0),
    unavailable_dates    date[] NOT NULL DEFAULT '{}',
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS inventory_product_id_idx
    ON seller.inventory (product_id);
