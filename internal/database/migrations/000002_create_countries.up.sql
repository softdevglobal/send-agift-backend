-- +migrate Up
CREATE SCHEMA IF NOT EXISTS core;

CREATE TABLE IF NOT EXISTS core.countries (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    iso_code          text NOT NULL UNIQUE,
    name              text NOT NULL,
    default_currency  text NOT NULL,
    default_timezone  text NOT NULL,
    status            text NOT NULL DEFAULT 'active',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_countries_status ON core.countries (status);
