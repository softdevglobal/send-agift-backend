-- Official attempts and competitions reference sessions and versions with
-- ON DELETE RESTRICT, so clear everything that points at these games first.
DELETE FROM competition.score_submissions
WHERE competition_id IN (
    SELECT c.id
    FROM competition.competitions c
    JOIN competition.game_versions v ON v.id = c.game_version_id
    JOIN competition.games g ON g.id = v.game_id
    WHERE g.slug IN ('cricket', 'block-blast', 'sling-shot', 'hill-rider')
);

DELETE FROM competition.game_sessions
WHERE game_version_id IN (
    SELECT v.id
    FROM competition.game_versions v
    JOIN competition.games g ON g.id = v.game_id
    WHERE g.slug IN ('cricket', 'block-blast', 'sling-shot', 'hill-rider')
);

DELETE FROM competition.games WHERE slug IN ('cricket', 'block-blast', 'sling-shot', 'hill-rider');
