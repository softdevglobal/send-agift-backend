UPDATE competition.game_versions v
SET config = (v.config
        - 'width' - 'height' - 'volleys' - 'stagger_ticks' - 'max_volley'
        - 'volley_grow_every' - 'bomb_from_volley' - 'bomb_chance'
        - 'fruit_radius' - 'bomb_radius' - 'min_peak' - 'max_peak' - 'margin'
        - 'drift' - 'lives' - 'combo_window_ticks') || '{
    "tick_ms": 20, "lanes": 5, "throws": 48, "start_flight_ticks": 60,
    "min_flight_ticks": 24, "flight_step_ticks": 1, "gap_ticks": 14,
    "bomb_every_throws": 7, "points_per_fruit": 12, "combo_bonus": 6
}'::jsonb
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'fruit-slice'
  AND v.status = 'approved';

UPDATE competition.games
SET description = 'Swipe the lane to cut the gifts as they arc past. Cut a run for a growing bonus, and leave the bombs well alone.'
WHERE slug = 'fruit-slice';
