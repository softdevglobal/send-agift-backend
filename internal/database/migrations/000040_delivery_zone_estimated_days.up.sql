-- Seller-stated delivery time for each distance band (0 = same day).
ALTER TABLE seller.shop_delivery_zones
    ADD COLUMN IF NOT EXISTS estimated_days integer NOT NULL DEFAULT 1
    CHECK (estimated_days >= 0);
