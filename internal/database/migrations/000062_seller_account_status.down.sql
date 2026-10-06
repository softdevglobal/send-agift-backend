UPDATE seller.sellers SET status = 'active' WHERE status = 'suspended';

ALTER TABLE seller.sellers
    DROP CONSTRAINT IF EXISTS sellers_status_check;
