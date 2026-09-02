-- +migrate Up
CREATE SCHEMA IF NOT EXISTS core;

CREATE TABLE IF NOT EXISTS core.countries (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    iso_code          text NOT NULL,
    name              text NOT NULL,
    default_currency  text NOT NULL,
    default_timezone  text NOT NULL,
    status            text NOT NULL DEFAULT 'full'
                      CHECK (status IN (
                          'full',
                          'marketplace',
                          'customer_only',
                          'browse_only',
                          'blocked'
                      )),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS countries_iso_code_uq ON core.countries (iso_code);
CREATE INDEX IF NOT EXISTS countries_status_idx ON core.countries (status);

CREATE TABLE IF NOT EXISTS core.idempotency_keys (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    key            text NOT NULL UNIQUE,
    scope          text NOT NULL,
    request_hash   text,
    response_code  integer,
    response_body  jsonb,
    status         text NOT NULL DEFAULT 'processing'
                   CHECK (status IN ('processing', 'completed', 'failed')),
    expires_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_scope_status
    ON core.idempotency_keys (scope, status);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expires_at
    ON core.idempotency_keys (expires_at)
    WHERE expires_at IS NOT NULL;
