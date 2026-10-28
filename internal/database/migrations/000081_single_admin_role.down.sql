-- Puts the old default back. Which accounts were superadmins is not kept,
-- so existing admins stay admins.
ALTER TABLE admin.admin_users ALTER COLUMN role SET DEFAULT 'superadmin';
