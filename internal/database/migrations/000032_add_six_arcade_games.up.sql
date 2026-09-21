-- Six more skill games: Memory Match, Whack-a-Mole, Bubble Shooter,
-- Tower Blocks, Fruit Slice and Doodle Jump.
--
-- Same rules as every game here: deterministic from a server seed, replayed
-- server-side from the move log, integer maths throughout so the app and the
-- server never disagree by a rounding error.
--   * Memory Match: the grid is dealt from the seed, so everyone sharing a
--     seed memorises the same layout.
--   * Whack-a-Mole: the whole run of moles — hole and up/down ticks — is
--     drawn from the seed before the first tap.
--   * Bubble Shooter: the opening wall and every queued colour come from the
--     seed; pops cascade by dropping whatever the clear left unsupported.
--   * Tower Blocks: slab widths are dealt from the seed, like 2048's spawns.
--   * Fruit Slice: the throw schedule is seeded; bombs sit on a fixed cadence
--     so the round stays fair to replay, but their lane is drawn.
--   * Doodle Jump: the tower of ledges is seeded and visible ahead; only the
--     lane you land in is a choice.

INSERT INTO competition.games (slug, name, description, game_type, status)
VALUES
    ('memory-match',
     'Memory Match',
     'Flip the gift cards two at a time and pair them off. Matches in a row pay a growing bonus, and every turn costs a little.',
     'memory',
     'approved'),
    ('whack-a-mole',
     'Whack-a-Mole',
     'Tap the moles before they drop back down. They stay up for less time as the round goes on, and empty holes cost you.',
     'timing',
     'approved'),
    ('bubble-shooter',
     'Bubble Shooter',
     'Fire the queued colour up a column and pop clusters of three or more. Anything left hanging falls with them.',
     'puzzle',
     'approved'),
    ('tower-blocks',
     'Tower Blocks',
     'Drop slabs into the well and fill a row across to clear it. Leave a gap underneath and the space is wasted.',
     'puzzle',
     'approved'),
    ('fruit-slice',
     'Fruit Slice',
     'Swipe the lane to cut the gifts as they arc past. Cut a run for a growing bonus, and leave the bombs well alone.',
     'timing',
     'approved'),
    ('doodle-jump',
     'Doodle Jump',
     'Hop your way up the tower, one ledge at a time. You can only reach the lane you are in or the ones beside it.',
     'precision',
     'approved')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT g.id, '1.0.0', v.config::jsonb, 'approved', now()
FROM competition.games g
JOIN (VALUES
    ('memory-match', '{
        "pairs": 8, "columns": 4, "points_per_match": 20, "streak_bonus": 10,
        "turn_penalty": 1, "max_turns": 80, "min_ms_per_flip": 160,
        "session_ttl_seconds": 3600
    }'),
    ('whack-a-mole', '{
        "tick_ms": 20, "holes": 9, "moles": 40, "start_up_ticks": 46,
        "min_up_ticks": 16, "up_step_ticks": 1, "gap_ticks": 10,
        "points_per_hit": 10, "streak_bonus": 4, "miss_penalty": 3,
        "session_ttl_seconds": 3600
    }'),
    ('bubble-shooter', '{
        "columns": 7, "rows": 11, "start_rows": 4, "colors": 4,
        "min_cluster": 3, "points_per_bubble": 10, "combo_bonus": 5,
        "max_shots": 200, "min_ms_per_shot": 220, "session_ttl_seconds": 3600
    }'),
    ('tower-blocks', '{
        "columns": 6, "rows": 12, "max_width": 3, "points_per_piece": 4,
        "points_per_row": 40, "multi_row_bonus": 30, "max_pieces": 300,
        "min_ms_per_piece": 260, "session_ttl_seconds": 3600
    }'),
    ('fruit-slice', '{
        "tick_ms": 20, "lanes": 5, "throws": 48, "start_flight_ticks": 60,
        "min_flight_ticks": 24, "flight_step_ticks": 1, "gap_ticks": 14,
        "bomb_every_throws": 7, "points_per_fruit": 12, "combo_bonus": 6,
        "session_ttl_seconds": 3600
    }'),
    ('doodle-jump', '{
        "lanes": 5, "platforms": 120, "spring_every": 9, "points_per_hop": 8,
        "spring_bonus": 14, "height_bonus_every": 10, "height_bonus": 25,
        "min_ms_per_hop": 200, "session_ttl_seconds": 3600
    }')
) AS v(slug, config) ON v.slug = g.slug
ON CONFLICT (game_id, version) DO NOTHING;
