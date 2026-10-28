-- There is one admin role. Every superadmin becomes an admin, and new
-- admin accounts default to it.
UPDATE admin.admin_users SET role = 'admin', updated_at = now() WHERE role = 'superadmin';

ALTER TABLE admin.admin_users ALTER COLUMN role SET DEFAULT 'admin';
