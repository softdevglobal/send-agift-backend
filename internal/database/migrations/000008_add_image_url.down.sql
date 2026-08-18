ALTER TABLE seller.products DROP COLUMN IF EXISTS image_url;
ALTER TABLE seller.shops DROP COLUMN IF EXISTS image_url;
ALTER TABLE seller.sellers DROP COLUMN IF EXISTS image_url;
ALTER TABLE customer.customers DROP COLUMN IF EXISTS image_url;
ALTER TABLE admin.admin_users DROP COLUMN IF EXISTS image_url;