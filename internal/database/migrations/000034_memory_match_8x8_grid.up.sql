-- Memory Match grows again, from the 6x6 (18 pairs) board to an 8x8
-- (32 pairs) board.
--
-- Sessions already in flight keep their own snapshot of the config they were
-- dealt with, so this only changes what a session started after it gets.
UPDATE competition.game_versions v
SET config = jsonb_set(
    jsonb_set(
        jsonb_set(v.config, '{pairs}', '32'),
        '{columns}', '8'
    ),
    '{max_turns}', '320'
)
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'memory-match'
  AND v.status = 'approved';
