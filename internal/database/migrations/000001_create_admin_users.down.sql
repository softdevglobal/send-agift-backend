-- +migrate Down
DROP TABLE IF EXISTS admin.admin_users;
DROP SCHEMA IF EXISTS admin CASCADE;
