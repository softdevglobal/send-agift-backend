UPDATE competition.game_versions v
SET config = jsonb_set(v.config, '{spring_lift}', '2')
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'doodle-jump'
  AND v.status = 'approved';
