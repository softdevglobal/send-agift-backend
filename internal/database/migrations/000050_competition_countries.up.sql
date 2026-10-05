-- A competition can run in several countries at once. Each listed country
-- must pass its own gates (skill competitions, progressive prizes, chance
-- games, points usage) before the competition can be scheduled, and only
-- players from a listed country may enter.
CREATE TABLE IF NOT EXISTS competition.competition_countries (
    competition_id  uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE CASCADE,
    country_id      uuid NOT NULL REFERENCES core.countries (id) ON DELETE RESTRICT,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (competition_id, country_id)
);

CREATE INDEX IF NOT EXISTS competition_countries_country_idx
    ON competition.competition_countries (country_id);

INSERT INTO competition.competition_countries (competition_id, country_id)
SELECT id, country_id FROM competition.competitions
ON CONFLICT DO NOTHING;

DROP INDEX IF EXISTS competition.competitions_country_status_idx;
ALTER TABLE competition.competitions DROP COLUMN IF EXISTS country_id;
