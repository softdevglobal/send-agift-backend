UPDATE ads.packages SET validity_days = 365 WHERE validity_days IS NULL;
UPDATE ads.purchases SET validity_days = 365 WHERE validity_days IS NULL;
ALTER TABLE ads.packages ALTER COLUMN validity_days SET NOT NULL;
ALTER TABLE ads.purchases ALTER COLUMN validity_days SET NOT NULL;
