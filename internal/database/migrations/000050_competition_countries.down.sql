-- Back to one country per competition: each keeps the first of its
-- countries by name.
ALTER TABLE competition.competitions
    ADD COLUMN IF NOT EXISTS country_id uuid REFERENCES core.countries (id);

UPDATE competition.competitions c
SET country_id = (
    SELECT cc.country_id
    FROM competition.competition_countries cc
    INNER JOIN core.countries co ON co.id = cc.country_id
    WHERE cc.competition_id = c.id
    ORDER BY co.name
    LIMIT 1)
WHERE c.country_id IS NULL;

ALTER TABLE competition.competitions
    ALTER COLUMN country_id SET NOT NULL;

CREATE INDEX IF NOT EXISTS competitions_country_status_idx
    ON competition.competitions (country_id, status);

DROP TABLE IF EXISTS competition.competition_countries;
