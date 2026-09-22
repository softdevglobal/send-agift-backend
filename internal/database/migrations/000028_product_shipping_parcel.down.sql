-- +migrate Down
ALTER TABLE seller.products
    DROP COLUMN IF EXISTS parcel_length,
    DROP COLUMN IF EXISTS parcel_width,
    DROP COLUMN IF EXISTS parcel_height,
    DROP COLUMN IF EXISTS parcel_distance_unit,
    DROP COLUMN IF EXISTS parcel_weight,
    DROP COLUMN IF EXISTS parcel_mass_unit;
