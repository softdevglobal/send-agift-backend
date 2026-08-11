-- +migrate Up
CREATE SCHEMA IF NOT EXISTS seller;

CREATE TABLE IF NOT EXISTS seller.sellers (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    country_id           uuid NOT NULL REFERENCES core.countries(id),
    seller_type          text NOT NULL DEFAULT 'individual',
    legal_name           text NOT NULL,
    trading_name         text,
    email                citext NOT NULL UNIQUE,
    phone                text,
    password_hash        text NOT NULL,
    verification_status  text NOT NULL DEFAULT 'unverified',
    status               text NOT NULL DEFAULT 'active',
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS seller.seller_addresses (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id       uuid NOT NULL REFERENCES seller.sellers (id) ON DELETE CASCADE,
    country_id      uuid NOT NULL REFERENCES core.countries (id),
    label           text,
    address_type    text NOT NULL DEFAULT 'both'
                    CHECK (address_type IN ('pickup', 'return', 'both')),
    line1           text NOT NULL,
    line2           text,
    city            text NOT NULL,
    region          text,
    postal_code     text,
    latitude        numeric,
    longitude       numeric,
    is_default      boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_seller_addresses_seller_id
    ON seller.seller_addresses (seller_id);

CREATE TABLE IF NOT EXISTS seller.shops (
    id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id                 uuid NOT NULL REFERENCES seller.sellers (id) ON DELETE CASCADE,
    name                      text NOT NULL,
    slug                      text NOT NULL UNIQUE,
    description               text,
    return_address_mode       text NOT NULL DEFAULT 'shop',
    customer_visible_location text,
    status                    text NOT NULL DEFAULT 'active',
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),
    address_id                uuid REFERENCES seller.seller_addresses (id)
);

CREATE INDEX IF NOT EXISTS idx_shops_seller_id ON seller.shops (seller_id);

ALTER TABLE seller.shops
    ADD COLUMN IF NOT EXISTS address_id uuid REFERENCES seller.seller_addresses (id);
