-- Snake opens slower still, and scoring is what winds it up.
--
-- The opening tick goes from 220ms to 300ms — slow enough to place the first
-- turns without hurrying — and a gift now takes 12ms off instead of 6, so
-- every gift is felt. The drift with time is eased back to one millisecond
-- every 45 ticks, leaving the pace mostly in the player's hands.
UPDATE competition.game_versions v
SET config = jsonb_set(
    jsonb_set(
        jsonb_set(v.config, '{tick_ms}', '300'),
        '{speedup_ms_per_food}', '12'
    ),
    '{speedup_every_ticks}', '45'
)
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'snake'
  AND v.status = 'approved';
