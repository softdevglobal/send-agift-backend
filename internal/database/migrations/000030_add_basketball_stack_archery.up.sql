-- Three more skill games: Basketball, Stack Tower and Archery.
--
-- All three follow the same rules as the first games: deterministic from a
-- server seed, fully replayable server-side, and run on fixed numbered ticks
-- so a faster phone gives no advantage.
--   * Basketball: aim and power are whole numbers; the moving hoop and the
--     shooting spot are on screen before every shot.
--   * Stack Tower: the sliding floor's position is a pure function of the
--     tick, so the server replays exactly the drop the player made.
--   * Archery: the sight sways in a fixed figure, and the wind for each arrow
--     is shown before it is shot. With a shared competition seed every
--     entrant faces the same wind.
-- All motion uses integer maths so the app and the server never disagree by
-- a rounding error.

INSERT INTO competition.games (slug, name, description, game_type, status)
VALUES
    ('basketball',
     'Basketball',
     'Shoot hoops against the clock. Swipe to aim and set your power, lead the moving hoop, and chain baskets to catch fire.',
     'precision',
     'approved'),
    ('stack-tower',
     'Stack Tower',
     'Drop each sliding floor squarely on the one below. Overhangs are sliced off; perfect drops keep the width and a run of them grows it back.',
     'timing',
     'approved'),
    ('archery',
     'Archery',
     'Ten arrows at the target. Read the wind, steady the swaying sight and release at the right moment.',
     'precision',
     'approved')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT g.id, '1.0.0',
       '{
           "tick_ms": 50,
           "round_ticks": 900,
           "flight_ticks": 14,
           "shot_cooldown_ticks": 20,
           "aim_tolerance": 12,
           "power_tolerance": 7,
           "swish_aim_tolerance": 4,
           "swish_power_tolerance": 3,
           "makes_per_level": 4,
           "session_ttl_seconds": 3600
       }'::jsonb,
       'approved', now()
FROM competition.games g
WHERE g.slug = 'basketball'
ON CONFLICT (game_id, version) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT g.id, '1.0.0',
       '{
           "tick_ms": 20,
           "base_size": 100,
           "travel": 130,
           "start_period_ticks": 150,
           "min_period_ticks": 66,
           "period_step_ticks": 4,
           "perfect_tolerance": 4,
           "points_per_floor": 10,
           "perfect_bonus": 5,
           "grow_every_perfects": 4,
           "grow_amount": 6,
           "max_floors": 500,
           "max_ticks": 90000,
           "session_ttl_seconds": 3600
       }'::jsonb,
       'approved', now()
FROM competition.games g
WHERE g.slug = 'stack-tower'
ON CONFLICT (game_id, version) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT g.id, '1.0.0',
       '{
           "tick_ms": 30,
           "arrows": 10,
           "ring_width": 10,
           "max_wind": 25,
           "sway_amplitude": 16,
           "sway_period_x": 46,
           "sway_period_y": 64,
           "reload_ticks": 30,
           "max_ticks": 20000,
           "session_ttl_seconds": 3600
       }'::jsonb,
       'approved', now()
FROM competition.games g
WHERE g.slug = 'archery'
ON CONFLICT (game_id, version) DO NOTHING;
