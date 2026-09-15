-- Drop in dependency order: scores -> sessions -> versions -> games.
DROP TABLE IF EXISTS competition.game_scores;
DROP TABLE IF EXISTS competition.game_sessions;
DROP TABLE IF EXISTS competition.game_versions;
DROP TABLE IF EXISTS competition.games;

-- Only removes the schema when nothing else was added to it.
DROP SCHEMA IF EXISTS competition RESTRICT;
