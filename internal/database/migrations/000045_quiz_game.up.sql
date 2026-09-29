-- QUIZ, the last game type in the Progressive Prize spec (§2 "Game type").
--
-- A quiz is a skill game: every entrant answers the same questions and the
-- best verified score wins. Unlike the other skill games its content must
-- stay secret, so each round keeps its own questions here, with their
-- answers, and a play hands the device only the prompts, options and time
-- limits. The server scores the submitted answers itself, at submission and
-- again at finalisation.

ALTER TABLE competition.games
    DROP CONSTRAINT IF EXISTS games_game_type_check,
    ADD CONSTRAINT games_game_type_check
        CHECK (game_type IN ('puzzle', 'memory', 'sorting', 'timing', 'precision', 'chance', 'quiz'));

INSERT INTO competition.games (slug, name, description, game_type, status)
VALUES ('quiz', 'Quiz',
        'Answer each question before the timer runs out. Right answers score, fast right answers score more.',
        'quiz', 'approved')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT g.id, '1.0.0', '{"session_ttl_seconds": 1800}'::jsonb, 'approved', now()
FROM competition.games g
WHERE g.slug = 'quiz'
  AND NOT EXISTS (SELECT 1 FROM competition.game_versions v WHERE v.game_id = g.id);

CREATE TABLE IF NOT EXISTS competition.quiz_questions (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id      uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE CASCADE,
    position            integer NOT NULL CHECK (position >= 0),
    prompt              text NOT NULL CHECK (char_length(trim(prompt)) BETWEEN 1 AND 500),
    options             jsonb NOT NULL CHECK (jsonb_typeof(options) = 'array'
                                              AND jsonb_array_length(options) BETWEEN 2 AND 6),
    correct_index       integer NOT NULL CHECK (correct_index >= 0 AND correct_index < jsonb_array_length(options)),
    time_limit_seconds  integer NOT NULL DEFAULT 20 CHECK (time_limit_seconds BETWEEN 5 AND 120),
    UNIQUE (competition_id, position)
);
