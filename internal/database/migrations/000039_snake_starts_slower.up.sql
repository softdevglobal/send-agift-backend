-- Snake opens at a walk and winds up as it goes.
--
-- It used to start at 160ms a tick and only quicken as gifts were eaten, three
-- milliseconds at a time, so the opening was brisk and the ramp barely felt.
-- It now starts at 220ms, takes six milliseconds off per gift, and drifts one
-- millisecond faster every thirty ticks so a long round tightens even when the
-- player is not finding anything. Both run down to the same floor.
UPDATE competition.game_versions v
SET config = jsonb_set(
    jsonb_set(
        jsonb_set(
            jsonb_set(v.config, '{tick_ms}', '220'),
            '{min_tick_ms}', '70'
        ),
        '{speedup_ms_per_food}', '6'
    ),
    '{speedup_every_ticks}', '30'
)
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'snake'
  AND v.status = 'approved';
