-- Fruit Slice drops its lanes for free swiping.
--
-- Fruit is now tossed up from below the board on real arcs, several at a
-- time, and the blade cuts whatever the finger actually passes through. A
-- move is "<tick>:<stroke>:<x1>:<y1>:<x2>:<y2>" on a 1000 x 1800 field. Fruit
-- cut in the same stroke pay a growing combo; three fruit falling uncut end
-- the round, as does cutting a bomb.
UPDATE competition.game_versions v
SET config = (v.config - 'lanes' - 'throws' - 'bomb_every_throws') || '{
    "tick_ms": 20, "width": 1000, "height": 1800, "volleys": 50,
    "start_flight_ticks": 120, "min_flight_ticks": 80, "flight_step_ticks": 1,
    "stagger_ticks": 8, "gap_ticks": 20, "max_volley": 5,
    "volley_grow_every": 6, "bomb_from_volley": 3, "bomb_chance": 3,
    "fruit_radius": 86, "bomb_radius": 76, "min_peak": 1250, "max_peak": 1650,
    "margin": 150, "drift": 250, "lives": 3, "points_per_fruit": 10,
    "combo_bonus": 5, "combo_window_ticks": 15
}'::jsonb
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'fruit-slice'
  AND v.status = 'approved';

UPDATE competition.games
SET description = 'Swipe to slice the fruit as it flies. Cut several in one stroke for a combo, don''t let three fall, and never touch a bomb.'
WHERE slug = 'fruit-slice';
