-- +migrate Up
-- Parcel size/weight for checkout delivery quotes (AliExpress-style) and seller rates prefills.
ALTER TABLE seller.products
    ADD COLUMN IF NOT EXISTS parcel_length text,
    ADD COLUMN IF NOT EXISTS parcel_width text,
    ADD COLUMN IF NOT EXISTS parcel_height text,
    ADD COLUMN IF NOT EXISTS parcel_distance_unit text,
    ADD COLUMN IF NOT EXISTS parcel_weight text,
    ADD COLUMN IF NOT EXISTS parcel_mass_unit text;
