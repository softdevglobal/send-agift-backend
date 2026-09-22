DROP INDEX IF EXISTS competition.idx_game_scores_leaderboard;
CREATE INDEX IF NOT EXISTS idx_game_scores_leaderboard
    ON competition.game_scores (game_version_id, score DESC, created_at ASC)
    WHERE validation_status = 'accepted';

DROP TABLE IF EXISTS finance.prize_reserves;
DROP TABLE IF EXISTS competition.prize_claims;
DROP TABLE IF EXISTS competition.competition_winners;
DROP TABLE IF EXISTS competition.leaderboard_snapshots;
DROP TABLE IF EXISTS competition.score_submissions;
DROP FUNCTION IF EXISTS competition.guard_score_submission();
DROP TABLE IF EXISTS competition.competition_attempts;

ALTER TABLE competition.game_sessions DROP CONSTRAINT IF EXISTS game_sessions_official_is_customer;
ALTER TABLE competition.game_sessions DROP CONSTRAINT IF EXISTS game_sessions_competition_fk;
DELETE FROM competition.game_scores
WHERE session_id IN (SELECT id FROM competition.game_sessions WHERE mode = 'official');
DELETE FROM competition.game_sessions WHERE mode = 'official';

DROP TABLE IF EXISTS competition.competitions;

DROP TABLE IF EXISTS admin.audit_log;
DROP FUNCTION IF EXISTS admin.forbid_audit_mutation();
DROP SCHEMA IF EXISTS finance RESTRICT;
