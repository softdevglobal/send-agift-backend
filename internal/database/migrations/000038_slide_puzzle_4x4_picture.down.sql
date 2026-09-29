UPDATE competition.game_versions v
SET config = jsonb_set(
    jsonb_set(
        jsonb_set(v.config, '{size}', '3'),
        '{shuffle_moves}', '80'
    ),
    '{solve_base}', '5000'
)
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'slide-puzzle'
  AND v.status = 'approved';
