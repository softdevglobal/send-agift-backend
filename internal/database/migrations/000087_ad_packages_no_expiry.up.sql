-- Advertising packages run until their budget of game entries is used up.
-- validity_days NULL means no time limit (and a purchase gets no
-- expires_at). Packages and purchases that already had a number of days
-- keep it: a seller keeps the terms they bought.
ALTER TABLE ads.packages ALTER COLUMN validity_days DROP NOT NULL;
ALTER TABLE ads.purchases ALTER COLUMN validity_days DROP NOT NULL;
