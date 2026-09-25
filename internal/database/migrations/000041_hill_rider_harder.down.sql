UPDATE competition.game_versions v
SET config = v.config || '{
    "hill_amp": 150, "hill_ramp": 3, "engine": 3, "start_fuel": 900,
    "fuel_can_every": 12, "launch_k": 1200000, "crash_slope": 150
}'::jsonb
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'hill-rider'
  AND v.status = 'approved';
