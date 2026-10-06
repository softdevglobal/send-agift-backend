-- Admin review uses the column that already exists on seller.sellers.
-- Signup leaves it unverified. Verified, rejected, and pending are the other values.

ALTER TABLE seller.sellers
    DROP CONSTRAINT IF EXISTS sellers_verification_status_check;

ALTER TABLE seller.sellers
    ADD CONSTRAINT sellers_verification_status_check
    CHECK (verification_status IN ('unverified', 'pending', 'verified', 'rejected'));
