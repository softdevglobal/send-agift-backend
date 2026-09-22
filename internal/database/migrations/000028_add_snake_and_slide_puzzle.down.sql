-- Sessions reference versions with ON DELETE RESTRICT, so clear plays first.
DELETE FROM competition.game_sessions
WHERE game_version_id IN (
    SELECT v.id
    FROM competition.game_versions v
    JOIN competition.games g ON g.id = v.game_id
    WHERE g.slug IN ('snake', 'slide-puzzle')
);

DELETE FROM competition.games WHERE slug IN ('snake', 'slide-puzzle');

ALTER TABLE competition.game_scores DROP COLUMN IF EXISTS stats;
