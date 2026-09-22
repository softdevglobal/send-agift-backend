ALTER TABLE marketplace.shipments
    ADD COLUMN IF NOT EXISTS parcel_details jsonb;
