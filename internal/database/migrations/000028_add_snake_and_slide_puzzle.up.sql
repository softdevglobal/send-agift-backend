-- Two more skill games, and a generic place for per-game result details.
--
-- Both games follow the same rule as 2048: deterministic from a server seed
-- and fully replayable server-side.
--   * Snake runs on fixed numbered ticks rather than frames, so a faster phone
--     gives no advantage and the server can replay it tick for tick.
--   * Slide Puzzle is perfect-information — every tile is visible — and its
--     scramble is built from legal moves, so every seed is solvable.
--
-- Memory-match style games were deliberately left out: their first flips are
-- guesses, which would let chance decide results.

-- highest_tile only makes sense for 2048. Other games report their details
-- (snake length, puzzle moves, ...) here.
ALTER TABLE competition.game_scores
    ADD COLUMN IF NOT EXISTS stats jsonb NOT NULL DEFAULT '{}'::jsonb;

INSERT INTO competition.games (slug, name, description, game_type, status)
VALUES
    ('snake',
     'Snake',
     'Steer the snake to the gift boxes and grow without hitting the walls or yourself. The board moves on fixed ticks, so no phone is faster than another.',
     'timing',
     'approved'),
    ('slide-puzzle',
     'Slide Puzzle',
     'Slide the tiles back into order in as few moves as you can. Every tile is visible from the start — pure logic.',
     'puzzle',
     'approved')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT g.id, '1.0.0',
       '{
           "grid_size": 15,
           "start_length": 3,
           "tick_ms": 160,
           "min_tick_ms": 80,
           "speedup_ms_per_food": 3,
           "points_per_food": 10,
           "max_ticks": 20000,
           "session_ttl_seconds": 3600
       }'::jsonb,
       'approved', now()
FROM competition.games g
WHERE g.slug = 'snake'
ON CONFLICT (game_id, version) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT g.id, '1.0.0',
       '{
           "size": 3,
           "shuffle_moves": 80,
           "solve_base": 5000,
           "move_penalty": 20,
           "solved_min_score": 1000,
           "min_ms_per_move": 80,
           "max_moves": 3000,
           "session_ttl_seconds": 3600
       }'::jsonb,
       'approved', now()
FROM competition.games g
WHERE g.slug = 'slide-puzzle'
ON CONFLICT (game_id, version) DO NOTHING;
