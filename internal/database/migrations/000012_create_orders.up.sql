CREATE SCHEMA IF NOT EXISTS marketplace;

CREATE TABLE IF NOT EXISTS marketplace.orders (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_number        text NOT NULL UNIQUE,
    customer_id         uuid NOT NULL REFERENCES customer.customers (id),
    recipient_id        uuid REFERENCES customer.recipients (id) ON DELETE SET NULL,
    country_id          uuid NOT NULL REFERENCES core.countries (id),
    customer_type       text NOT NULL DEFAULT 'personal'
                        CHECK (customer_type IN ('personal', 'corporate')),
    delivery_date       date NOT NULL,
    status              text NOT NULL DEFAULT 'draft'
                        CHECK (status IN (
                            'draft',
                            'pending_payment',
                            'paid',
                            'accepted',
                            'preparing',
                            'dispatched',
                            'delivered',
                            'cancelled',
                            'refunded'
                        )),
    subtotal_amount     integer NOT NULL DEFAULT 0 CHECK (subtotal_amount >= 0),
    delivery_amount     integer NOT NULL DEFAULT 0 CHECK (delivery_amount >= 0),
    total_amount        integer NOT NULL DEFAULT 0 CHECK (total_amount >= 0),
    currency            text NOT NULL,
    gift_message        text,
    -- FK to media.media_assets when that table exists
    media_greeting_id   uuid,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_orders_customer_id
    ON marketplace.orders (customer_id);

CREATE INDEX IF NOT EXISTS idx_orders_recipient_id
    ON marketplace.orders (recipient_id);

CREATE INDEX IF NOT EXISTS idx_orders_status
    ON marketplace.orders (status);

CREATE TABLE IF NOT EXISTS marketplace.order_items (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id            uuid NOT NULL REFERENCES marketplace.orders (id) ON DELETE CASCADE,
    seller_id           uuid NOT NULL REFERENCES seller.sellers (id),
    shop_id             uuid NOT NULL REFERENCES seller.shops (id),
    product_id          uuid NOT NULL REFERENCES seller.products (id),
    quantity            integer NOT NULL CHECK (quantity > 0),
    unit_amount         integer NOT NULL CHECK (unit_amount >= 0),
    total_amount        integer NOT NULL CHECK (total_amount >= 0),
    fulfilment_status   text NOT NULL DEFAULT 'pending'
                        CHECK (fulfilment_status IN (
                            'pending',
                            'accepted',
                            'preparing',
                            'ready',
                            'dispatched',
                            'delivered',
                            'cancelled'
                        )),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_order_items_order_id
    ON marketplace.order_items (order_id);

CREATE INDEX IF NOT EXISTS idx_order_items_seller_id
    ON marketplace.order_items (seller_id);

CREATE INDEX IF NOT EXISTS idx_order_items_shop_id
    ON marketplace.order_items (shop_id);

CREATE INDEX IF NOT EXISTS idx_order_items_product_id
    ON marketplace.order_items (product_id);
