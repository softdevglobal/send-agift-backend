UPDATE competition.game_versions v
SET config = jsonb_set(
    jsonb_set(
        jsonb_set(v.config, '{tick_ms}', '220'),
        '{speedup_ms_per_food}', '6'
    ),
    '{speedup_every_ticks}', '30'
)
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'snake'
  AND v.status = 'approved';
