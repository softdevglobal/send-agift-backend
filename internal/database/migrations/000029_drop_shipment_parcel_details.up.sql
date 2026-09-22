-- Parcel dims/weight live on seller.products (000028). shipments.parcel_details
-- was a duplicate cache filled at /shipping/rates; product.parcel is the source of truth.
ALTER TABLE marketplace.shipments
    DROP COLUMN IF EXISTS parcel_details;
