-- A Doodle Jump spring now carries the climber three ledges, counting the
-- spring itself, rather than two — so it clears the two above it outright and
-- reads as a launch rather than a long step.
--
-- Sessions already in flight keep their own snapshot of the config they
-- started with, so this only changes rounds begun after it.
UPDATE competition.game_versions v
SET config = jsonb_set(v.config, '{spring_lift}', '3')
FROM competition.games g
WHERE v.game_id = g.id
  AND g.slug = 'doodle-jump'
  AND v.status = 'approved';
