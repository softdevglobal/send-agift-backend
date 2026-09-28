-- The rest of the Progressive Prize spec: chance mechanics, points earning,
-- and the fraud signals they need.
--
-- Chance mechanics (spin wheel, scratch card, treasure hunt, instant win,
-- prize draw) share one server-side engine: the outcome of every play is drawn
-- with a cryptographically secure generator at the moment of play, inside the
-- play transaction, and kept with the play as auditable metadata. The app
-- only reveals it. A prize draw picks its winners from the entries at close,
-- and the whole draw is recorded.
--
-- Paid-entry games of chance with a prize are regulated as gambling or
-- lotteries in most places, so they have their own per-country gate, off by
-- default, separate from skill competitions and from growing prizes.

ALTER TABLE core.country_capabilities
    ADD COLUMN IF NOT EXISTS chance_games_enabled boolean NOT NULL DEFAULT false;

-- ─── Catalog ──────────────────────────────────────────────────────────────
ALTER TABLE competition.games
    DROP CONSTRAINT IF EXISTS games_game_type_check,
    ADD CONSTRAINT games_game_type_check
        CHECK (game_type IN ('puzzle', 'memory', 'sorting', 'timing', 'precision', 'chance'));

INSERT INTO competition.games (slug, name, description, game_type, status)
VALUES
    ('spin-wheel', 'Spin the Wheel',
     'Spin once per play. Land on the jackpot and the prize is yours.', 'chance', 'approved'),
    ('scratch-card', 'Scratch Card',
     'Scratch the card to reveal it. Three jackpots in a row wins the prize.', 'chance', 'approved'),
    ('treasure-hunt', 'Treasure Hunt',
     'Open a chest to see what the play found.', 'chance', 'approved'),
    ('instant-win', 'Instant Win',
     'Open the gift to find out straight away whether it won.', 'chance', 'approved'),
    ('prize-draw', 'Prize Draw',
     'Every play is one entry. Winners are drawn at random when the round closes.', 'chance', 'approved')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO competition.game_versions (game_id, version, config, status, approved_at)
SELECT g.id, '1.0.0', v.config::jsonb, 'approved', now()
FROM competition.games g
JOIN (VALUES
    ('spin-wheel', '{"mechanic": "spin", "segments": 8, "session_ttl_seconds": 600}'),
    ('scratch-card', '{"mechanic": "scratch", "cells": 9, "session_ttl_seconds": 600}'),
    ('treasure-hunt', '{"mechanic": "treasure", "chests": 3, "session_ttl_seconds": 600}'),
    ('instant-win', '{"mechanic": "instant", "session_ttl_seconds": 600}'),
    ('prize-draw', '{"mechanic": "draw", "session_ttl_seconds": 600}')
) AS v(slug, config) ON v.slug = g.slug
WHERE NOT EXISTS (SELECT 1 FROM competition.game_versions x WHERE x.game_id = g.id);

-- ─── Rounds ───────────────────────────────────────────────────────────────
-- Instant rounds: each play wins with probability 1 in win_odds.
ALTER TABLE competition.competitions
    ADD COLUMN IF NOT EXISTS win_odds integer CHECK (win_odds BETWEEN 2 AND 100000000);

-- The outcome of a chance play, with what it was drawn from.
ALTER TABLE competition.competition_attempts
    ADD COLUMN IF NOT EXISTS result_payload jsonb;

-- Chance winners come from a play, not a score.
ALTER TABLE competition.competition_winners
    ALTER COLUMN score_submission_id DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS attempt_id uuid REFERENCES competition.competition_attempts (id) ON DELETE RESTRICT;

ALTER TABLE competition.competition_winners
    DROP CONSTRAINT IF EXISTS competition_winners_has_source,
    ADD CONSTRAINT competition_winners_has_source
        CHECK (score_submission_id IS NOT NULL OR attempt_id IS NOT NULL);

-- A prize draw, kept in full so anyone can check it: the ordered entries it
-- was drawn from (and their hash), the random values used and who they
-- picked. One per round.
CREATE TABLE IF NOT EXISTS competition.prize_draws (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id      uuid NOT NULL UNIQUE REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    entry_count         integer NOT NULL CHECK (entry_count >= 0),
    entries_sha256      text NOT NULL,
    entries             jsonb NOT NULL,
    random_values       jsonb NOT NULL,
    picks               jsonb NOT NULL,
    algorithm           text NOT NULL,
    drawn_by_admin_id   uuid,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION competition.forbid_prize_draw_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'competition.prize_draws is append-only';
END;
$$;

DROP TRIGGER IF EXISTS prize_draws_append_only ON competition.prize_draws;
CREATE TRIGGER prize_draws_append_only
    BEFORE UPDATE OR DELETE ON competition.prize_draws
    FOR EACH ROW EXECUTE FUNCTION competition.forbid_prize_draw_mutation();

-- Device and account concentration (spec §10 fraud signals).
CREATE INDEX IF NOT EXISTS competition_attempts_device_idx
    ON competition.competition_attempts ((risk_metadata ->> 'device_id'))
    WHERE risk_metadata ? 'device_id';

-- ─── Points earning ───────────────────────────────────────────────────────
-- Per-country earning rules, set by a Super Admin without a deployment.
-- Points are earned on delivered orders (and taken back if the order is
-- refunded), plus an optional sign-up bonus for customers who join after the
-- bonus was switched on.
CREATE TABLE IF NOT EXISTS finance.points_earning_rules (
    country_id            uuid PRIMARY KEY REFERENCES core.countries (id) ON DELETE CASCADE,
    enabled               boolean NOT NULL DEFAULT false,
    -- Points per whole unit of the order's currency (1.00), rounded down.
    points_per_unit       integer NOT NULL DEFAULT 0 CHECK (points_per_unit BETWEEN 0 AND 10000),
    -- Only orders delivered after this earn, so switching a rule on does not
    -- pay out the whole order history.
    effective_from        timestamptz NOT NULL DEFAULT now(),
    signup_bonus          integer NOT NULL DEFAULT 0 CHECK (signup_bonus BETWEEN 0 AND 1000000),
    signup_bonus_since    timestamptz NOT NULL DEFAULT now(),
    updated_by_admin_id   uuid,
    updated_at            timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE finance.points_ledger
    ADD COLUMN IF NOT EXISTS order_id uuid REFERENCES marketplace.orders (id) ON DELETE RESTRICT,
    DROP CONSTRAINT IF EXISTS points_ledger_entry_type_check,
    ADD CONSTRAINT points_ledger_entry_type_check CHECK (entry_type IN (
        'admin_grant', 'admin_deduction', 'play_debit', 'play_refund', 'correction',
        'order_reward', 'order_reversal', 'signup_bonus')),
    DROP CONSTRAINT IF EXISTS points_ledger_sign,
    ADD CONSTRAINT points_ledger_sign CHECK (
        (entry_type IN ('play_debit', 'admin_deduction', 'order_reversal') AND amount_delta < 0) OR
        (entry_type IN ('play_refund', 'admin_grant', 'order_reward', 'signup_bonus') AND amount_delta > 0) OR
        entry_type = 'correction');
