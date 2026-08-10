-- +migrate Up
CREATE EXTENSION IF NOT EXISTS citext;

CREATE SCHEMA IF NOT EXISTS customer;

CREATE TABLE IF NOT EXISTS customer.customers (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    country_id            uuid NOT NULL REFERENCES core.countries(id),
    email                 citext NOT NULL UNIQUE,
    phone                 text,
    password_hash         text NOT NULL,
    display_name          text,
    customer_type         text NOT NULL DEFAULT 'individual',
    date_of_birth         date,
    age_verified_at       timestamptz,
    identity_verified_at  timestamptz,
    status                text NOT NULL DEFAULT 'active',
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz
);

CREATE INDEX IF NOT EXISTS idx_customers_country_id ON customer.customers (country_id);
CREATE INDEX IF NOT EXISTS idx_customers_status ON customer.customers (status);

CREATE TABLE IF NOT EXISTS customer.customer_addresses (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id   uuid NOT NULL REFERENCES customer.customers(id) ON DELETE CASCADE,
    country_id    uuid NOT NULL REFERENCES core.countries(id),
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

CREATE INDEX IF NOT EXISTS idx_customer_addresses_customer_id ON customer.customer_addresses (customer_id);
