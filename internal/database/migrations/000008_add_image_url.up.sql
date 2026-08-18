ALTER TABLE admin.admin_users
    ADD COLUMN IF NOT EXISTS image_url text;

ALTER TABLE customer.customers
    ADD COLUMN IF NOT EXISTS image_url text;

ALTER TABLE seller.sellers
    ADD COLUMN IF NOT EXISTS image_url text;

ALTER TABLE seller.shops
    ADD COLUMN IF NOT EXISTS image_url text;

ALTER TABLE seller.products
    ADD COLUMN IF NOT EXISTS image_url text;