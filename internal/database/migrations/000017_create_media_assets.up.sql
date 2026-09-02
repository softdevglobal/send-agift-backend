-- +migrate Up
CREATE SCHEMA IF NOT EXISTS media;

CREATE TABLE IF NOT EXISTS media.media_assets (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_type          text NOT NULL
                        CHECK (owner_type IN ('customer', 'seller', 'admin', 'system')),
    owner_id            uuid,
    asset_type          text NOT NULL
                        CHECK (asset_type IN ('image', 'video', 'audio', 'document', 'label')),
    bucket              text NOT NULL,
    object_path         text NOT NULL,
    cdn_url             text,
    mime_type           text NOT NULL,
    size_bytes          bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    processing_status   text NOT NULL DEFAULT 'uploaded'
                        CHECK (processing_status IN ('uploaded', 'processing', 'ready', 'failed', 'rejected')),
    moderation_status   text NOT NULL DEFAULT 'pending'
                        CHECK (moderation_status IN ('pending', 'approved', 'rejected')),
    metadata            jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_media_assets_owner
    ON media.media_assets (owner_type, owner_id);

CREATE INDEX IF NOT EXISTS idx_media_assets_asset_type
    ON media.media_assets (asset_type);

CREATE TABLE IF NOT EXISTS core.country_payment_providers (
    id                           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    country_id                   uuid NOT NULL REFERENCES core.countries (id),
    provider                     text NOT NULL,
    mode                         text NOT NULL DEFAULT 'test'
                                 CHECK (mode IN ('test', 'live', 'paused')),
    seller_onboarding_enabled    boolean NOT NULL DEFAULT false,
    seller_payouts_enabled       boolean NOT NULL DEFAULT false,
    written_approval_status      text NOT NULL DEFAULT 'missing'
                                 CHECK (written_approval_status IN (
                                     'missing', 'requested', 'approved', 'rejected'
                                 )),
    approval_document_media_id   uuid REFERENCES media.media_assets (id),
    created_at                   timestamptz NOT NULL DEFAULT now(),
    updated_at                   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (country_id, provider)
);

CREATE INDEX IF NOT EXISTS idx_country_payment_providers_country_id
    ON core.country_payment_providers (country_id);
