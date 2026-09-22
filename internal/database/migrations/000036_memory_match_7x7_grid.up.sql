-- Memory Match takes the full 7x7 square: 49 slots, 24 pairs, 48 cards.
--
-- 49 cards could never all be dealt as pairs, since it is odd. The client
-- fills the odd slot in the middle of the grid with an emblem that is not a
-- card and cannot be flipped, so the board reads as a complete 7x7 while
-- every card on it still has exactly one partner.
UPDATE competition.game_versions v
SET config = jsonb_set(
    jsonb_set(
        jsonb_set(v.config, '{pairs}', '24'),
        '{columns}', '7'
    ),
    '{max_turns}', '240'
)
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'memory-match'
  AND v.status = 'approved';
