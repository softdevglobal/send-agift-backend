-- Reverses 000048. Plays go back to whatever the running code charges.
ALTER TABLE competition.games DROP COLUMN IF EXISTS play_cost_points;
