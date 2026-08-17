CREATE TABLE IF NOT EXISTS customer.saved_gifts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id  uuid NOT NULL REFERENCES customer.customers (id) ON DELETE CASCADE,
    product_id   uuid NOT NULL REFERENCES seller.products (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (customer_id, product_id)
);

CREATE INDEX IF NOT EXISTS saved_gifts_customer_id_idx
    ON customer.saved_gifts (customer_id);

CREATE INDEX IF NOT EXISTS saved_gifts_product_id_idx
    ON customer.saved_gifts (product_id);