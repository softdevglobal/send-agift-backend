UPDATE competition.game_versions v
SET config = jsonb_set(
    jsonb_set(
        jsonb_set(v.config, '{pairs}', '18'),
        '{columns}', '6'
    ),
    '{max_turns}', '180'
)
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'memory-match'
  AND v.status = 'approved';
