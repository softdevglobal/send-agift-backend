CREATE TABLE IF NOT EXISTS customer.recipients (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id         uuid NOT NULL REFERENCES customer.customers (id) ON DELETE CASCADE,
    name                text NOT NULL,
    relationship        text,
    email               citext,
    phone               text,
    image_url           text,
    default_address_id  uuid,
    preferences         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS customer.recipient_addresses (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    recipient_id  uuid NOT NULL REFERENCES customer.recipients (id) ON DELETE CASCADE,
    country_id    uuid NOT NULL REFERENCES core.countries (id),
    label         text,
    address_type  text NOT NULL DEFAULT 'shipping',
    line1         text NOT NULL,
    line2         text,
    city          text NOT NULL,
    region        text,
    postal_code   text,
    latitude      numeric,
    longitude     numeric,
    is_default    boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE customer.recipients
    ADD CONSTRAINT recipients_default_address_id_fkey
    FOREIGN KEY (default_address_id)
    REFERENCES customer.recipient_addresses (id)
    ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_recipients_customer_id
    ON customer.recipients (customer_id);

CREATE INDEX IF NOT EXISTS idx_recipient_addresses_recipient_id
    ON customer.recipient_addresses (recipient_id);
