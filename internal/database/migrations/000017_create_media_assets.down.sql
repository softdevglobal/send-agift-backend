-- +migrate Down
DROP TABLE IF EXISTS core.country_payment_providers;
DROP TABLE IF EXISTS media.media_assets;
DROP SCHEMA IF EXISTS media;
