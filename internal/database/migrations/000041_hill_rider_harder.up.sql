-- Hill Rider gets a much harder road.
--
-- The hills grow taller and sooner: the biggest step between two knots goes
-- from 150 to 220, and the course reaches it by knot 28 rather than 42. The
-- engine is a touch stronger (3 -> 4) so the tallest climbs can still be
-- taken with a run-up, which also means the car arrives at crests faster.
-- Crests throw it sooner (launch_k 1200000 -> 1000000), landings must match
-- the ground more closely (crash_slope 150 -> 130), and fuel is scarcer: a
-- smaller tank (900 -> 800) and cans every 16 knots instead of 12.
UPDATE competition.game_versions v
SET config = v.config || '{
    "hill_amp": 220, "hill_ramp": 7, "engine": 4, "start_fuel": 800,
    "fuel_can_every": 16, "launch_k": 1000000, "crash_slope": 130
}'::jsonb
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'hill-rider'
  AND v.status = 'approved';
