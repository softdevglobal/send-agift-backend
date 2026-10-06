-- Admin can suspend a seller and turn them back on. "deleted" stays the
-- seller's own close and is not reopened from the admin screen.

ALTER TABLE seller.sellers
    DROP CONSTRAINT IF EXISTS sellers_status_check;

ALTER TABLE seller.sellers
    ADD CONSTRAINT sellers_status_check
    CHECK (status IN ('active', 'suspended', 'deleted'));
