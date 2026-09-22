-- Memory Match settles on a 7-wide, 6-row board: 42 cards, 21 pairs.
--
-- An 8x8 board was 64 cards, which is more than reads comfortably on a phone.
-- A true 7x7 is 49 cards, an odd number, so it cannot be dealt as pairs at all
-- — 7x6 is the closest board to it that can.
--
-- Sessions already in flight keep their own snapshot of the config they were
-- dealt with, so this only changes what a session started after it gets.
UPDATE competition.game_versions v
SET config = jsonb_set(
    jsonb_set(
        jsonb_set(v.config, '{pairs}', '21'),
        '{columns}', '7'
    ),
    '{max_turns}', '210'
)
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'memory-match'
  AND v.status = 'approved';
