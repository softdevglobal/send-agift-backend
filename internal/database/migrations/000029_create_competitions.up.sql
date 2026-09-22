-- Skill competitions, per the SendAgift Database Structure doc §11, plus the
-- prize reserve (§9.5) and admin audit log (§13.3) they depend on.
--
-- Rules this schema enforces (Master Plan §13–§20):
--   * One server seed per competition. Every entrant plays identical content,
--     because a different board per player would be chance, which the rules
--     forbid (§14.1–§14.2). The seed is only handed out once the competition
--     is live.
--   * The score that counts is the server's replay of the move log. The move
--     log itself is kept so top scores can be re-verified at finalisation.
--   * Score submissions are append-only; only review fields may change later.
--   * A competition cannot open without a funded prize reserve, published
--     rules, and skill competitions enabled for its country.
--   * Points per attempt are recorded now; deducting them waits for the points
--     ledger, so points_ledger_id has no foreign key yet.

CREATE SCHEMA IF NOT EXISTS admin;
CREATE SCHEMA IF NOT EXISTS finance;

-- ─── Audit log (append-only) ──────────────────────────────────────────────
-- Every competition admin action is recorded here (critical rule #10:
-- Super Admin controls must be audited).
CREATE TABLE IF NOT EXISTS admin.audit_log (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_type   text NOT NULL CHECK (actor_type IN ('admin', 'system', 'customer', 'seller')),
    actor_id     uuid,
    action       text NOT NULL,
    entity_type  text NOT NULL,
    entity_id    uuid,
    before_data  jsonb,
    after_data   jsonb,
    reason       text,
    ip_address   inet,
    user_agent   text,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_log_entity_idx
    ON admin.audit_log (entity_type, entity_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_actor_idx
    ON admin.audit_log (actor_type, actor_id, created_at DESC);

CREATE OR REPLACE FUNCTION admin.forbid_audit_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'admin.audit_log is append-only';
END;
$$;

DROP TRIGGER IF EXISTS audit_log_append_only ON admin.audit_log;
CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE ON admin.audit_log
    FOR EACH ROW EXECUTE FUNCTION admin.forbid_audit_mutation();

-- ─── Competitions ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS competition.competitions (
    id                              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    country_id                      uuid NOT NULL REFERENCES core.countries (id),
    game_version_id                 uuid NOT NULL REFERENCES competition.game_versions (id) ON DELETE RESTRICT,
    title                           text NOT NULL CHECK (char_length(trim(title)) BETWEEN 3 AND 120),

    -- live and closed are also derived from the clock; the stored value is
    -- brought up to date whenever competitions are read.
    status                          text NOT NULL DEFAULT 'draft'
                                    CHECK (status IN ('draft', 'scheduled', 'live', 'closed',
                                                      'frozen', 'finalised', 'cancelled')),
    starts_at                       timestamptz NOT NULL,
    ends_at                         timestamptz NOT NULL,
    timezone                        text NOT NULL,

    -- Shared by every attempt: identical conditions for every entrant.
    server_seed                     text NOT NULL,

    points_per_attempt              integer NOT NULL DEFAULT 0 CHECK (points_per_attempt >= 0),
    max_attempts_per_customer       integer NOT NULL CHECK (max_attempts_per_customer BETWEEN 1 AND 100),
    min_age                         integer NOT NULL DEFAULT 18,
    requires_identity_verification  boolean NOT NULL DEFAULT true,
    number_of_winners               integer NOT NULL DEFAULT 1 CHECK (number_of_winners BETWEEN 1 AND 100),

    prize_description               text NOT NULL CHECK (char_length(trim(prize_description)) > 0),
    prize_value_amount              bigint CHECK (prize_value_amount >= 0), -- minor units
    prize_currency                  text,

    -- Must be published in the app before the competition opens (§13.3).
    official_rules                  text,
    official_rules_media_id         uuid,

    cancel_reason                   text CHECK (cancel_reason IN (
                                        'technical_failure', 'security_breach', 'legal_direction',
                                        'provider_or_store_direction', 'platform_outage',
                                        'prize_unavailable', 'fairness_failure')),
    cancel_note                     text,
    cancelled_at                    timestamptz,
    frozen_at                       timestamptz,
    finalised_at                    timestamptz,

    created_by_admin_id             uuid,
    created_at                      timestamptz NOT NULL DEFAULT now(),
    updated_at                      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT competitions_time_order CHECK (ends_at > starts_at),
    CONSTRAINT competitions_min_age CHECK (min_age >= 18),
    CONSTRAINT competitions_cancel_has_reason CHECK (status <> 'cancelled' OR cancel_reason IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS competitions_status_starts_idx
    ON competition.competitions (status, starts_at);
CREATE INDEX IF NOT EXISTS competitions_country_status_idx
    ON competition.competitions (country_id, status);

-- Official sessions belong to a competition and to a signed-in customer.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'game_sessions_competition_fk') THEN
        ALTER TABLE competition.game_sessions
            ADD CONSTRAINT game_sessions_competition_fk
            FOREIGN KEY (competition_id) REFERENCES competition.competitions (id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'game_sessions_official_is_customer') THEN
        ALTER TABLE competition.game_sessions
            ADD CONSTRAINT game_sessions_official_is_customer
            CHECK (mode <> 'official' OR (competition_id IS NOT NULL AND customer_id IS NOT NULL));
    END IF;
END $$;

-- ─── Attempts ─────────────────────────────────────────────────────────────
-- One official attempt. The play itself runs on a game_session (seed, expiry,
-- ownership, idempotent submit); this row carries the competition side.
CREATE TABLE IF NOT EXISTS competition.competition_attempts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id    uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    customer_id       uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    session_id        uuid NOT NULL UNIQUE REFERENCES competition.game_sessions (id) ON DELETE RESTRICT,
    -- Links the points debit once the points ledger exists.
    points_ledger_id  uuid,
    points_spent      integer NOT NULL DEFAULT 0 CHECK (points_spent >= 0),
    attempt_number    integer NOT NULL CHECK (attempt_number >= 1),
    server_seed       text NOT NULL,
    status            text NOT NULL DEFAULT 'started'
                      CHECK (status IN ('started', 'submitted', 'accepted', 'rejected', 'voided')),
    void_reason       text,
    started_at        timestamptz NOT NULL DEFAULT now(),
    submitted_at      timestamptz,
    idempotency_key   text NOT NULL UNIQUE,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- Two simultaneous starts race for the same attempt number; one loses,
    -- so the attempt limit can never be exceeded.
    UNIQUE (competition_id, customer_id, attempt_number)
);

CREATE INDEX IF NOT EXISTS competition_attempts_customer_idx
    ON competition.competition_attempts (competition_id, customer_id);

-- ─── Score submissions (append-only) ──────────────────────────────────────
CREATE TABLE IF NOT EXISTS competition.score_submissions (
    id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id             uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    attempt_id                 uuid NOT NULL UNIQUE REFERENCES competition.competition_attempts (id) ON DELETE RESTRICT,
    customer_id                uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,

    -- Computed by the server's replay; the only score that counts.
    score                      bigint NOT NULL CHECK (score >= 0),
    client_score               bigint,
    -- Server-measured play time: the approved secondary skill metric that
    -- breaks score ties (§14.4). Lower is better.
    duration_ms                bigint NOT NULL CHECK (duration_ms >= 0),
    moves_count                integer NOT NULL DEFAULT 0 CHECK (moves_count >= 0),
    stats                      jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- The exact move log, kept so leading scores can be replayed again at
    -- finalisation, plus its hash as a tamper check.
    event_log                  jsonb NOT NULL,
    event_log_hash             text NOT NULL,
    event_log_media_id         uuid,

    validation_status          text NOT NULL DEFAULT 'pending'
                               CHECK (validation_status IN ('pending', 'accepted', 'rejected', 'manual_review')),
    review_reason              text,
    reviewed_by_admin_id       uuid,
    reviewed_at                timestamptz,

    -- Reserved for the Redis / Pub/Sub pipeline.
    redis_rank_at_submit       integer,
    persisted_from_message_id  text,

    idempotency_key            text NOT NULL UNIQUE,
    created_at                 timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS score_submissions_leaderboard_idx
    ON competition.score_submissions (competition_id, validation_status, score DESC, duration_ms ASC, created_at ASC);

-- Once written, a score can only be reviewed, never rewritten or removed.
CREATE OR REPLACE FUNCTION competition.guard_score_submission() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'score submissions cannot be deleted';
    END IF;
    IF NEW.competition_id IS DISTINCT FROM OLD.competition_id
       OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
       OR NEW.customer_id IS DISTINCT FROM OLD.customer_id
       OR NEW.score IS DISTINCT FROM OLD.score
       OR NEW.client_score IS DISTINCT FROM OLD.client_score
       OR NEW.duration_ms IS DISTINCT FROM OLD.duration_ms
       OR NEW.moves_count IS DISTINCT FROM OLD.moves_count
       OR NEW.stats IS DISTINCT FROM OLD.stats
       OR NEW.event_log IS DISTINCT FROM OLD.event_log
       OR NEW.event_log_hash IS DISTINCT FROM OLD.event_log_hash
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'only review fields of a score submission may change';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS score_submissions_guard ON competition.score_submissions;
CREATE TRIGGER score_submissions_guard
    BEFORE UPDATE OR DELETE ON competition.score_submissions
    FOR EACH ROW EXECUTE FUNCTION competition.guard_score_submission();

-- ─── Leaderboard snapshots ────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS competition.leaderboard_snapshots (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id       uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    snapshot_type        text NOT NULL CHECK (snapshot_type IN ('close', 'freeze', 'final')),
    snapshot_data        jsonb NOT NULL,
    redis_version        text,
    created_by_admin_id  uuid,
    created_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (competition_id, snapshot_type)
);

-- ─── Winners ──────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS competition.competition_winners (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id       uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    customer_id          uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    score_submission_id  uuid NOT NULL REFERENCES competition.score_submissions (id) ON DELETE RESTRICT,
    -- Which prize this row is for (1 = first prize).
    prize_position       integer NOT NULL CHECK (prize_position >= 1),
    -- Where the player finished on the final leaderboard.
    rank                 integer NOT NULL CHECK (rank >= 1),
    status               text NOT NULL DEFAULT 'pending_validation'
                         CHECK (status IN ('pending_validation', 'validated', 'disqualified', 'unclaimed', 'replaced')),
    -- Why a winner was disqualified or marked unclaimed; the original row is
    -- preserved and the next eligible player gets a new one (§19.3).
    status_reason        text,
    validated_at         timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (competition_id, customer_id)
);

-- One active winner per prize position.
CREATE UNIQUE INDEX IF NOT EXISTS competition_winners_active_position_uq
    ON competition.competition_winners (competition_id, prize_position)
    WHERE status IN ('pending_validation', 'validated');

-- ─── Prize claims ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS competition.prize_claims (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    winner_id            uuid NOT NULL UNIQUE REFERENCES competition.competition_winners (id) ON DELETE RESTRICT,
    customer_id          uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    claim_deadline_at    timestamptz NOT NULL, -- 14 days from validation
    claimed_at           timestamptz,
    status               text NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'claimed', 'verified', 'fulfilled', 'expired', 'rejected')),
    delivery_address_id  uuid REFERENCES customer.customer_addresses (id) ON DELETE RESTRICT,
    terms_accepted_at    timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- ─── Prize reserves ───────────────────────────────────────────────────────
-- Funded separately from seller settlement money, before the competition
-- opens, and never dependent on points or attempts (§13.12, §20.3).
CREATE TABLE IF NOT EXISTS finance.prize_reserves (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id      uuid NOT NULL UNIQUE REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    reserve_amount      bigint NOT NULL CHECK (reserve_amount >= 0), -- minor units
    currency            text NOT NULL,
    funding_source      text NOT NULL CHECK (funding_source IN ('sendagift', 'approved_sponsor')),
    status              text NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'funded', 'released', 'cancelled')),
    -- Escrow, purchase or insurance proof the admin recorded when funding.
    evidence_reference  text,
    funded_at           timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT prize_reserves_funded_has_evidence
        CHECK (status <> 'funded' OR (evidence_reference IS NOT NULL AND funded_at IS NOT NULL))
);

-- ─── Practice leaderboard tie-break ───────────────────────────────────────
-- Ties are broken by server-measured play time rather than by who submitted
-- first (§14.4), for practice boards as well.
DROP INDEX IF EXISTS competition.idx_game_scores_leaderboard;
CREATE INDEX IF NOT EXISTS idx_game_scores_leaderboard
    ON competition.game_scores (game_version_id, score DESC, duration_ms ASC, created_at ASC)
    WHERE validation_status = 'accepted';
