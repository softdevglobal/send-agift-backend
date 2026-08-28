-- +migrate Up
CREATE TABLE IF NOT EXISTS core.country_capabilities (
    id                               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    country_id                       uuid NOT NULL UNIQUE REFERENCES core.countries (id),
    customer_registration_enabled    boolean NOT NULL DEFAULT true,
    seller_registration_enabled      boolean NOT NULL DEFAULT true,
    seller_payouts_enabled           boolean NOT NULL DEFAULT true,
    domestic_delivery_enabled        boolean NOT NULL DEFAULT true,
    international_delivery_enabled   boolean NOT NULL DEFAULT true,
    memberships_enabled              boolean NOT NULL DEFAULT true,
    points_earning_enabled           boolean NOT NULL DEFAULT true,
    points_usage_enabled             boolean NOT NULL DEFAULT true,
    skill_competitions_enabled       boolean NOT NULL DEFAULT false,
    app_store_available              boolean NOT NULL DEFAULT true,
    rule_version                     integer NOT NULL DEFAULT 1,
    created_at                       timestamptz NOT NULL DEFAULT now(),
    updated_at                       timestamptz NOT NULL DEFAULT now()
);
