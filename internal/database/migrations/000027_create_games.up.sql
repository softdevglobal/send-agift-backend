-- Skill-game foundation: game catalog, versioned rules, seeded sessions and
-- server-validated scores.
--
-- Compliance notes (Master Plan v3.2 §14, Architecture §7):
--   * Games must be DETERMINISTIC. Every spawned tile comes from a
--     server-generated seed, never from client-side randomness, so that every
--     entrant playing the same seed faces identical conditions.
--   * The client score is NEVER trusted. The backend replays the submitted
--     move log and recomputes the authoritative score.
--   * Phase 1 is PRACTICE ONLY: no points are deducted and there is no prize
--     leaderboard. competition_id is reserved for official attempts once the
--     points ledger and competitions land.

CREATE SCHEMA IF NOT EXISTS competition;

-- ─── Game catalog ─────────────────────────────────────────────────────────
-- One row per distinct game (2048, memory, sorting, ...).
CREATE TABLE IF NOT EXISTS competition.games (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Stable public identifier used in URLs: /games/{slug}
    slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,48}[a-z0-9]$'),
    name        text NOT NULL,
    description text,
    -- Broad family, used for grouping in the app.
    game_type   text NOT NULL CHECK (game_type IN ('puzzle', 'memory', 'sorting', 'timing', 'precision')),
    status      text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'approved', 'retired')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_games_status ON competition.games (status);

-- ─── Game versions ────────────────────────────────────────────────────────
-- Rules are versioned because a competition locks one exact version for its
-- whole runtime; changing scoring mid-competition is forbidden.
CREATE TABLE IF NOT EXISTS competition.game_versions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id      uuid NOT NULL REFERENCES competition.games (id) ON DELETE CASCADE,
    -- Semantic version, e.g. 1.0.0
    version      text NOT NULL,
    -- Rules the SERVER enforces when replaying a session. The client receives
    -- the same object so both sides run identical mechanics.
    -- For 2048: {"board_size":4,"spawn_four_percent":10,"start_tiles":2,
    --            "win_tile":2048,"max_moves":5000,"min_ms_per_move":40}
    config       jsonb NOT NULL DEFAULT '{}'::jsonb,
    status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'qa', 'approved', 'retired')),
    approved_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (game_id, version)
);

-- Only one approved version per game may be served to clients at a time.
CREATE UNIQUE INDEX IF NOT EXISTS uq_game_versions_one_approved
    ON competition.game_versions (game_id)
    WHERE status = 'approved';

-- ─── Game sessions ────────────────────────────────────────────────────────
-- One row per play. Created BEFORE play starts so the seed and start time are
-- server-owned facts, never client claims.
CREATE TABLE IF NOT EXISTS competition.game_sessions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_version_id uuid NOT NULL REFERENCES competition.game_versions (id) ON DELETE RESTRICT,

    -- Identity: logged-in customer XOR guest token (same rule as reel social).
    customer_id     uuid REFERENCES customer.customers (id) ON DELETE CASCADE,
    guest_token     text,

    -- practice deducts no points and never reaches a prize leaderboard.
    -- official is reserved for competition attempts (phase 2).
    mode            text NOT NULL DEFAULT 'practice' CHECK (mode IN ('practice', 'official')),
    -- Reserved for phase 2; always NULL while only practice exists.
    competition_id  uuid,

    -- Server-generated seed. The client derives every tile spawn from this, so
    -- the backend can reproduce the exact same game during replay.
    server_seed     text NOT NULL,
    -- Snapshot of the version config at session start, so later config edits
    -- cannot retroactively change how an in-flight session is scored.
    config          jsonb NOT NULL DEFAULT '{}'::jsonb,

    status          text NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'submitted', 'expired', 'abandoned')),

    started_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    submitted_at    timestamptz,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- Exactly one identity must be set.
    CHECK (
        (customer_id IS NOT NULL AND guest_token IS NULL)
        OR (customer_id IS NULL AND guest_token IS NOT NULL)
    ),
    -- Practice never belongs to a competition.
    CHECK (mode <> 'practice' OR competition_id IS NULL)
);

CREATE INDEX IF NOT EXISTS idx_game_sessions_customer
    ON competition.game_sessions (customer_id, started_at DESC)
    WHERE customer_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_game_sessions_guest
    ON competition.game_sessions (guest_token, started_at DESC)
    WHERE guest_token IS NOT NULL;

-- Sweeping expired sessions.
CREATE INDEX IF NOT EXISTS idx_game_sessions_active_expiry
    ON competition.game_sessions (expires_at)
    WHERE status = 'active';

-- ─── Game scores ──────────────────────────────────────────────────────────
-- The authoritative score, produced by the SERVER replaying the move log.
-- One score per session, enforced by the primary key, which also makes
-- submission naturally idempotent.
CREATE TABLE IF NOT EXISTS competition.game_scores (
    session_id        uuid PRIMARY KEY REFERENCES competition.game_sessions (id) ON DELETE CASCADE,
    game_version_id   uuid NOT NULL REFERENCES competition.game_versions (id) ON DELETE RESTRICT,

    -- Denormalized identity so leaderboards do not need to join sessions.
    customer_id       uuid REFERENCES customer.customers (id) ON DELETE CASCADE,
    guest_token       text,

    -- Score the SERVER computed by replaying the moves. This is the only
    -- number that counts.
    score             bigint NOT NULL CHECK (score >= 0),
    -- Score the CLIENT claimed. Stored purely as a cheat signal; a mismatch
    -- means the app is modified or out of sync.
    client_score      bigint,
    -- Highest tile reached, e.g. 2048.
    highest_tile      integer NOT NULL DEFAULT 0 CHECK (highest_tile >= 0),
    moves_count       integer NOT NULL DEFAULT 0 CHECK (moves_count >= 0),
    duration_ms       bigint NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),

    -- SHA-256 of the exact submitted move log; lets us prove later what was
    -- replayed without keeping the full log forever.
    move_log_hash     text NOT NULL,

    validation_status text NOT NULL DEFAULT 'accepted'
                      CHECK (validation_status IN ('accepted', 'rejected', 'manual_review')),
    -- Why a score was rejected or flagged, e.g. "client score mismatch".
    review_reason     text,

    created_at        timestamptz NOT NULL DEFAULT now(),

    CHECK (
        (customer_id IS NOT NULL AND guest_token IS NULL)
        OR (customer_id IS NULL AND guest_token IS NOT NULL)
    )
);

-- Leaderboard read path: best accepted scores for a game version.
CREATE INDEX IF NOT EXISTS idx_game_scores_leaderboard
    ON competition.game_scores (game_version_id, score DESC, created_at ASC)
    WHERE validation_status = 'accepted';

-- Personal best lookups.
CREATE INDEX IF NOT EXISTS idx_game_scores_customer_best
    ON competition.game_scores (customer_id, game_version_id, score DESC)
    WHERE customer_id IS NOT NULL AND validation_status = 'accepted';

CREATE INDEX IF NOT EXISTS idx_game_scores_guest_best
    ON competition.game_scores (guest_token, game_version_id, score DESC)
    WHERE guest_token IS NOT NULL AND validation_status = 'accepted';

-- ─── Seed the first game ──────────────────────────────────────────────────
-- 2048 is the launch game: turn-based (no device-speed advantage), fully
-- deterministic from a seed, and completely replayable server-side.
INSERT INTO competition.games (slug, name, description, game_type, status)
VALUES (
    '2048',
    '2048',
    'Slide and merge matching tiles to reach 2048. Pure logic, no chance: every tile you are dealt comes from the same server seed for every player.',
    'puzzle',
    'approved'
)
ON CONFLICT (slug) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT
    g.id,
    '1.0.0',
    '{
        "board_size": 4,
        "start_tiles": 2,
        "spawn_four_percent": 10,
        "win_tile": 2048,
        "max_moves": 5000,
        "min_ms_per_move": 40,
        "session_ttl_seconds": 3600
    }'::jsonb,
    'approved',
    now()
FROM competition.games g
WHERE g.slug = '2048'
ON CONFLICT (game_id, version) DO NOTHING;
