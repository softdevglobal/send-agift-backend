-- +migrate Down
DROP TABLE IF EXISTS core.idempotency_keys;
DROP TABLE IF EXISTS core.countries;
