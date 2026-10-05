-- Same-day cutoffs are read in the shop's timezone. Existing shops
-- inherit their country's default zone; sellers can change it later.
ALTER TABLE seller.shops
    ADD COLUMN IF NOT EXISTS timezone text;

UPDATE seller.shops s
SET timezone = c.default_timezone
FROM core.countries c
WHERE c.id = s.country_id
  AND (s.timezone IS NULL OR btrim(s.timezone) = '');

UPDATE seller.shops
SET timezone = 'UTC'
WHERE timezone IS NULL OR btrim(timezone) = '';

ALTER TABLE seller.shops
    ALTER COLUMN timezone SET DEFAULT 'UTC';

ALTER TABLE seller.shops
    ALTER COLUMN timezone SET NOT NULL;
