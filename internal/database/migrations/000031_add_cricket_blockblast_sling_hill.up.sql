-- Four more skill games: Cricket, Block Blast, Sling Shot and Hill Rider.
--
-- Same rules as every game here: deterministic from a server seed, replayed
-- server-side from the move log, integer maths throughout so the app and the
-- server never disagree by a rounding error.
--   * Cricket: every delivery (pace and line) and the field are drawn from
--     the seed and shown before each ball; timing and shot direction decide
--     the runs.
--   * Block Blast: pieces are dealt from the seed, like 2048's tile spawns —
--     identical for everyone who shares a seed.
--   * Sling Shot: the structures are visible before the first shot, and the
--     flight and collapse are integer physics.
--   * Hill Rider: the hills are drawn from the seed and visible ahead; the
--     drive runs on fixed ticks like Snake.

INSERT INTO competition.games (slug, name, description, game_type, status)
VALUES
    ('cricket',
     'Cricket',
     'Face twelve balls with three wickets in hand. Time your swing to the delivery and aim for the gaps in the field.',
     'timing',
     'approved'),
    ('block-blast',
     'Block Blast',
     'Drop pieces on the board and fill rows and columns to blast them away. Chain clears for combo bonuses.',
     'puzzle',
     'approved'),
    ('sling-shot',
     'Sling Shot',
     'Pull back the sling and knock the target blocks off their towers. Wood breaks, stone does not — find the weak spot.',
     'precision',
     'approved'),
    ('hill-rider',
     'Hill Rider',
     'Drive as far as you can over rolling hills. Mind your fuel, and do not take the crests too fast.',
     'timing',
     'approved')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT g.id, '1.0.0', v.config::jsonb, 'approved', now()
FROM competition.games g
JOIN (VALUES
    ('cricket', '{
        "tick_ms": 20, "balls": 12, "wickets": 3, "ball_cycle_ticks": 160,
        "runup_ticks": 45, "min_travel_ticks": 38, "max_travel_ticks": 62,
        "perfect_window": 1, "good_window": 3, "edge_window": 6,
        "early_ticks": 12, "late_ticks": 8, "fielders": 5, "fielder_reach": 10,
        "max_angle": 80, "session_ttl_seconds": 3600
    }'),
    ('block-blast', '{
        "board_size": 8, "hand_size": 3, "points_per_cell": 1,
        "points_per_line": 10, "combo_bonus": 5, "min_ms_per_move": 250,
        "max_moves": 3000, "session_ttl_seconds": 3600
    }'),
    ('sling-shot', '{
        "tick_ms": 20, "levels": 8, "shots_per_level": 3, "gravity": 6,
        "launch_scale": 3, "max_pull": 100, "max_flight_ticks": 360,
        "target_points": 500, "wood_points": 50, "shot_bonus": 300,
        "min_ms_per_shot": 700, "session_ttl_seconds": 3600
    }'),
    ('hill-rider', '{
        "tick_ms": 20, "knot_spacing": 200, "knots": 600, "start_fuel": 900,
        "fuel_can_every": 12, "engine": 3, "brake": 4, "slope_gravity": 4,
        "air_gravity": 3, "friction": 1, "max_speed": 150, "launch_k": 1200000,
        "crash_slope": 150, "max_ticks": 30000, "session_ttl_seconds": 3600
    }')
) AS v(slug, config) ON v.slug = g.slug
ON CONFLICT (game_id, version) DO NOTHING;
