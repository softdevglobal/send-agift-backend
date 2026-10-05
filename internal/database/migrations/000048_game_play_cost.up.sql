-- What one practice play of each game costs, in points, set per game by a
-- Super Admin. Every game starts at 50. The flat price plays had before.
-- and 0 makes a game free to play.
ALTER TABLE competition.games
    ADD COLUMN IF NOT EXISTS play_cost_points integer NOT NULL DEFAULT 50
        CHECK (play_cost_points BETWEEN 0 AND 1000000);
