-- The slide puzzle becomes a 4x4 picture rather than a 3x3 of numbers.
--
-- Fifteen tiles take far more moves than eight: eighty or so played perfectly,
-- a few hundred played well. The base and the penalty are widened to keep
-- those apart, instead of a good solve bottoming out on the floor halfway
-- through. Only a solved board scores at all, so the leaderboard is finishers
-- ordered by how few moves it took them.
UPDATE competition.game_versions v
SET config = jsonb_set(
    jsonb_set(
        jsonb_set(v.config, '{size}', '4'),
        '{shuffle_moves}', '140'
    ),
    '{solve_base}', '14000'
)
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'slide-puzzle'
  AND v.status = 'approved';
