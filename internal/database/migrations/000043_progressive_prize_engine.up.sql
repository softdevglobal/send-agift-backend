-- Progressive Prize Game Engine (Developer Specification v1.0, 25 Sep 2026).
--
-- A competition is the spec's "game round": it can start from a Super
-- Admin-defined prize and grow by a fixed amount on every eligible play, paid
-- for in points. Every one of the sixteen skill games runs on it unchanged;
-- only the money around a play is new.
--
-- The rules this schema enforces:
--   * The prize is a ledger, not a counter. competition.prize_ledger is
--     append-only and the source of truth; competitions.current_prize_cents
--     is a cache kept in the same transaction (spec §1.2–§1.3).
--   * Points are a ledger too. finance.points_accounts holds the balance,
--     which can never go negative; finance.points_ledger records every change
--     and cannot be edited or deleted.
--   * A play is one transaction: points debit, attempt, prize increment and
--     cached prize move together or not at all (§4). The client's idempotency
--     key is unique per customer, so a retry can never charge twice (§4.2).
--   * The prize can never pass its cap, enforced by the database as well as
--     the service (AC-08).
--   * Money is integer minor units throughout (AC-14).
--   * Progressive prizes need their own per-country compliance approval, off
--     by default (spec "Compliance design hook").

-- ─── Compliance gate ──────────────────────────────────────────────────────
ALTER TABLE core.country_capabilities
    ADD COLUMN IF NOT EXISTS progressive_prizes_enabled boolean NOT NULL DEFAULT false;

-- ─── Points wallet ────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS finance.points_accounts (
    customer_id      uuid PRIMARY KEY REFERENCES customer.customers (id) ON DELETE RESTRICT,
    balance          bigint NOT NULL DEFAULT 0 CHECK (balance >= 0),
    lifetime_earned  bigint NOT NULL DEFAULT 0 CHECK (lifetime_earned >= 0),
    lifetime_spent   bigint NOT NULL DEFAULT 0 CHECK (lifetime_spent >= 0),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS finance.points_ledger (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seq              bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    customer_id      uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    entry_type       text NOT NULL CHECK (entry_type IN (
                         'admin_grant', 'admin_deduction', 'play_debit', 'play_refund', 'correction')),
    amount_delta     bigint NOT NULL CHECK (amount_delta <> 0),
    balance_after    bigint NOT NULL CHECK (balance_after >= 0),
    competition_id   uuid REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    attempt_id       uuid,
    -- One key per intended change: a retried request finds its first row.
    idempotency_key  text NOT NULL UNIQUE,
    reason           text,
    actor_type       text NOT NULL CHECK (actor_type IN ('admin', 'system', 'customer')),
    actor_id         uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT points_ledger_sign CHECK (
        (entry_type IN ('play_debit', 'admin_deduction') AND amount_delta < 0) OR
        (entry_type IN ('play_refund', 'admin_grant') AND amount_delta > 0) OR
        entry_type = 'correction'),
    CONSTRAINT points_ledger_reason CHECK (
        entry_type NOT IN ('admin_grant', 'admin_deduction', 'play_refund', 'correction')
        OR char_length(trim(coalesce(reason, ''))) > 0)
);

CREATE INDEX IF NOT EXISTS points_ledger_customer_idx
    ON finance.points_ledger (customer_id, seq DESC);
CREATE INDEX IF NOT EXISTS points_ledger_competition_idx
    ON finance.points_ledger (competition_id, entry_type)
    WHERE competition_id IS NOT NULL;

CREATE OR REPLACE FUNCTION finance.forbid_points_ledger_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'finance.points_ledger is append-only; post a compensating entry instead';
END;
$$;

DROP TRIGGER IF EXISTS points_ledger_append_only ON finance.points_ledger;
CREATE TRIGGER points_ledger_append_only
    BEFORE UPDATE OR DELETE ON finance.points_ledger
    FOR EACH ROW EXECUTE FUNCTION finance.forbid_points_ledger_mutation();

-- ─── Competitions become progressive-capable rounds ───────────────────────
ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_status_check,
    ADD CONSTRAINT competitions_status_check CHECK (status IN (
        'draft', 'scheduled', 'live', 'paused', 'closed', 'frozen', 'finalised', 'cancelled')),
    DROP CONSTRAINT IF EXISTS competitions_max_attempts_per_customer_check,
    ADD CONSTRAINT competitions_max_attempts_per_customer_check
        CHECK (max_attempts_per_customer BETWEEN 1 AND 10000);

ALTER TABLE competition.competitions
    ADD COLUMN IF NOT EXISTS prize_growth_enabled      boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS prize_type                text NOT NULL DEFAULT 'cash'
        CHECK (prize_type IN ('cash', 'product', 'voucher', 'gift', 'other')),
    ADD COLUMN IF NOT EXISTS winner_method             text NOT NULL DEFAULT 'score'
        CHECK (winner_method IN ('score', 'instant', 'draw', 'admin_approved')),
    ADD COLUMN IF NOT EXISTS start_prize_cents         bigint NOT NULL DEFAULT 0 CHECK (start_prize_cents >= 0),
    ADD COLUMN IF NOT EXISTS increment_per_play_cents  bigint NOT NULL DEFAULT 0 CHECK (increment_per_play_cents >= 0),
    ADD COLUMN IF NOT EXISTS max_prize_cents           bigint CHECK (max_prize_cents > 0),
    -- Whether plays still go on once the prize has hit its cap (they then add
    -- nothing to it).
    ADD COLUMN IF NOT EXISTS continue_at_cap           boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS daily_play_limit          integer CHECK (daily_play_limit BETWEEN 1 AND 10000),
    -- Fewest plays a customer needs before they can be declared a winner.
    ADD COLUMN IF NOT EXISTS min_plays_to_win          integer CHECK (min_plays_to_win BETWEEN 1 AND 10000),
    ADD COLUMN IF NOT EXISTS current_prize_cents       bigint NOT NULL DEFAULT 0 CHECK (current_prize_cents >= 0),
    ADD COLUMN IF NOT EXISTS eligible_play_count       bigint NOT NULL DEFAULT 0 CHECK (eligible_play_count >= 0),
    ADD COLUMN IF NOT EXISTS unique_player_count       bigint NOT NULL DEFAULT 0 CHECK (unique_player_count >= 0),
    -- Bumped on every prize movement; live updates carry it so a client can
    -- drop an event older than what it already shows.
    ADD COLUMN IF NOT EXISTS prize_version             bigint NOT NULL DEFAULT 0,
    -- The prize at close, before any payout, for the closed-state screens.
    ADD COLUMN IF NOT EXISTS final_prize_cents         bigint CHECK (final_prize_cents >= 0),
    ADD COLUMN IF NOT EXISTS round_no                  integer NOT NULL DEFAULT 1 CHECK (round_no >= 1),
    ADD COLUMN IF NOT EXISTS previous_round_id         uuid REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    -- Optimistic lock for admin edits.
    ADD COLUMN IF NOT EXISTS config_version            integer NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS paused_at                 timestamptz,
    ADD COLUMN IF NOT EXISTS closed_at                 timestamptz,
    ADD COLUMN IF NOT EXISTS updated_by_admin_id       uuid;

ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_prize_cap,
    ADD CONSTRAINT competitions_prize_cap CHECK (
        max_prize_cents IS NULL OR
        (max_prize_cents >= start_prize_cents AND current_prize_cents <= max_prize_cents));

-- ─── Plays ────────────────────────────────────────────────────────────────
-- An official attempt is the spec's game_play.
ALTER TABLE competition.competition_attempts
    ADD COLUMN IF NOT EXISTS client_request_id      text,
    ADD COLUMN IF NOT EXISTS prize_increment_cents  bigint NOT NULL DEFAULT 0 CHECK (prize_increment_cents >= 0),
    ADD COLUMN IF NOT EXISTS prize_before_cents     bigint CHECK (prize_before_cents >= 0),
    ADD COLUMN IF NOT EXISTS prize_after_cents      bigint CHECK (prize_after_cents >= 0),
    ADD COLUMN IF NOT EXISTS risk_metadata          jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS refunded_at            timestamptz;

CREATE UNIQUE INDEX IF NOT EXISTS competition_attempts_client_request_uq
    ON competition.competition_attempts (customer_id, client_request_id)
    WHERE client_request_id IS NOT NULL;

-- Daily limits count a customer's plays by start time.
CREATE INDEX IF NOT EXISTS competition_attempts_daily_idx
    ON competition.competition_attempts (competition_id, customer_id, started_at);

-- The debit and the play point at each other; both rows are written in one
-- transaction, so the checks wait for the commit.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'competition_attempts_points_ledger_fk') THEN
        ALTER TABLE competition.competition_attempts
            ADD CONSTRAINT competition_attempts_points_ledger_fk
            FOREIGN KEY (points_ledger_id) REFERENCES finance.points_ledger (id)
            ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'points_ledger_attempt_fk') THEN
        ALTER TABLE finance.points_ledger
            ADD CONSTRAINT points_ledger_attempt_fk
            FOREIGN KEY (attempt_id) REFERENCES competition.competition_attempts (id)
            ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
    END IF;
END $$;

-- ─── Winners carry their prize and its settlement ─────────────────────────
ALTER TABLE competition.competition_winners
    ADD COLUMN IF NOT EXISTS prize_value_cents     bigint CHECK (prize_value_cents >= 0),
    ADD COLUMN IF NOT EXISTS settlement_status     text NOT NULL DEFAULT 'pending'
        CHECK (settlement_status IN ('pending', 'settled', 'failed', 'not_applicable')),
    ADD COLUMN IF NOT EXISTS settlement_reference  text,
    ADD COLUMN IF NOT EXISTS settled_at            timestamptz;

-- ─── Prize ledger (append-only, the source of truth) ──────────────────────
CREATE TABLE IF NOT EXISTS competition.prize_ledger (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Posting order. Every write happens under the competition's row lock,
    -- so seq order is also the order balances were computed in.
    seq                  bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    competition_id       uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    attempt_id           uuid REFERENCES competition.competition_attempts (id)
                             ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    winner_id            uuid REFERENCES competition.competition_winners (id) ON DELETE RESTRICT,
    entry_type           text NOT NULL CHECK (entry_type IN (
                             'seed', 'play_increment', 'admin_adjustment', 'play_reversal',
                             'winner_settlement', 'correction')),
    amount_delta_cents   bigint NOT NULL,
    balance_after_cents  bigint NOT NULL CHECK (balance_after_cents >= 0),
    reason               text,
    actor_type           text NOT NULL CHECK (actor_type IN ('admin', 'system', 'customer')),
    actor_id             uuid,
    created_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT prize_ledger_sign CHECK (
        (entry_type = 'seed' AND amount_delta_cents >= 0) OR
        (entry_type = 'play_increment' AND amount_delta_cents > 0) OR
        (entry_type IN ('play_reversal', 'winner_settlement') AND amount_delta_cents < 0) OR
        (entry_type IN ('admin_adjustment', 'correction') AND amount_delta_cents <> 0)),
    CONSTRAINT prize_ledger_reason CHECK (
        entry_type NOT IN ('admin_adjustment', 'play_reversal', 'correction')
        OR char_length(trim(coalesce(reason, ''))) > 0),
    CONSTRAINT prize_ledger_links CHECK (
        (entry_type NOT IN ('play_increment', 'play_reversal') OR attempt_id IS NOT NULL) AND
        (entry_type <> 'winner_settlement' OR winner_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS prize_ledger_competition_idx
    ON competition.prize_ledger (competition_id, seq);
-- Exactly one seed per round (AC-02), one increment and at most one reversal
-- per play, one payout per winner row.
CREATE UNIQUE INDEX IF NOT EXISTS prize_ledger_one_seed_uq
    ON competition.prize_ledger (competition_id) WHERE entry_type = 'seed';
CREATE UNIQUE INDEX IF NOT EXISTS prize_ledger_one_increment_uq
    ON competition.prize_ledger (attempt_id) WHERE entry_type = 'play_increment';
CREATE UNIQUE INDEX IF NOT EXISTS prize_ledger_one_reversal_uq
    ON competition.prize_ledger (attempt_id) WHERE entry_type = 'play_reversal';
CREATE UNIQUE INDEX IF NOT EXISTS prize_ledger_one_settlement_uq
    ON competition.prize_ledger (winner_id) WHERE entry_type = 'winner_settlement';

CREATE OR REPLACE FUNCTION competition.forbid_prize_ledger_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'competition.prize_ledger is append-only; post a compensating entry instead';
END;
$$;

DROP TRIGGER IF EXISTS prize_ledger_append_only ON competition.prize_ledger;
CREATE TRIGGER prize_ledger_append_only
    BEFORE UPDATE OR DELETE ON competition.prize_ledger
    FOR EACH ROW EXECUTE FUNCTION competition.forbid_prize_ledger_mutation();

-- ─── Outbox ───────────────────────────────────────────────────────────────
-- Written inside the transaction that caused the event, so an event exists
-- if and only if its change committed. Live prize streams read from here.
CREATE TABLE IF NOT EXISTS core.outbox_events (
    seq             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id              uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    aggregate_type  text NOT NULL,
    aggregate_id    uuid NOT NULL,
    event_type      text NOT NULL,
    payload         jsonb NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    published_at    timestamptz
);

CREATE INDEX IF NOT EXISTS outbox_events_aggregate_idx
    ON core.outbox_events (aggregate_type, aggregate_id, seq);
CREATE INDEX IF NOT EXISTS outbox_events_unpublished_idx
    ON core.outbox_events (seq) WHERE published_at IS NULL;

-- ─── Analytics inputs ─────────────────────────────────────────────────────
-- Plays turned away, by reason (spec §10). Written outside the play
-- transaction, which rolled back; best effort.
CREATE TABLE IF NOT EXISTS competition.play_rejections (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    competition_id  uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE CASCADE,
    customer_id     uuid REFERENCES customer.customers (id) ON DELETE SET NULL,
    reason_code     text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS play_rejections_competition_idx
    ON competition.play_rejections (competition_id, reason_code);

-- Signed-in viewers of a round, for view → play conversion.
CREATE TABLE IF NOT EXISTS competition.competition_views (
    competition_id   uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE CASCADE,
    customer_id      uuid NOT NULL REFERENCES customer.customers (id) ON DELETE CASCADE,
    first_viewed_at  timestamptz NOT NULL DEFAULT now(),
    last_viewed_at   timestamptz NOT NULL DEFAULT now(),
    view_count       integer NOT NULL DEFAULT 1,
    PRIMARY KEY (competition_id, customer_id)
);

-- Reconciliation results (spec §9, AC-12). Discrepancies are kept, never
-- auto-corrected.
CREATE TABLE IF NOT EXISTS competition.reconciliation_runs (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    competition_id  uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE CASCADE,
    status          text NOT NULL CHECK (status IN ('ok', 'discrepancy')),
    details         jsonb NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS reconciliation_runs_competition_idx
    ON competition.reconciliation_runs (competition_id, created_at DESC);

-- ─── Backfill ─────────────────────────────────────────────────────────────
-- Existing competitions keep their fixed prize, now expressed on the ledger:
-- the start prize is the prize, and published rounds get their seed.
UPDATE competition.competitions
SET start_prize_cents = coalesce(prize_value_amount, 0),
    current_prize_cents = CASE WHEN status = 'draft' THEN 0 ELSE coalesce(prize_value_amount, 0) END
WHERE start_prize_cents = 0 AND prize_value_amount IS NOT NULL;

INSERT INTO competition.prize_ledger
    (competition_id, entry_type, amount_delta_cents, balance_after_cents, reason, actor_type)
SELECT c.id, 'seed', c.start_prize_cents, c.start_prize_cents,
       'fixed prize carried over when the prize ledger was introduced', 'system'
FROM competition.competitions c
WHERE c.status <> 'draft'
  AND NOT EXISTS (SELECT 1 FROM competition.prize_ledger l
                  WHERE l.competition_id = c.id AND l.entry_type = 'seed');
