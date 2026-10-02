-- A shop sells in one country. Gift prices and delivery ranges use that
-- country's currency, not the seller account's country.
ALTER TABLE seller.shops
    ADD COLUMN IF NOT EXISTS country_id uuid REFERENCES core.countries (id);

UPDATE seller.shops s
SET country_id = se.country_id
FROM seller.sellers se
WHERE se.id = s.seller_id
  AND s.country_id IS NULL;

ALTER TABLE seller.shops
    ALTER COLUMN country_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_shops_country_id ON seller.shops (country_id);
