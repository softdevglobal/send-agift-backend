-- Competition points (handover v4, 26 October 2026).
--
-- A separate unit from the points customers already hold. Qualifying
-- completed gifts earn ordinary competition points; a paid membership period
-- adds one extra bonus after its first qualifying gift; points are spent on
-- skill competitions through a reserve-then-capture entry.
--
-- What this keeps apart, as the handover asks:
--   * Legacy points (points.points_accounts / points_ledger) are untouched
--     and never converted. Competition points live in their own lots.
--   * Points and money never share a posting. Prize money stays in finance.
--   * Every spent point keeps the lot it came from, so its source and
--     restrictions survive to the entry that used it.
--
-- Nothing here is switched on. A missing approval record means unavailable,
-- and every competition stays on legacy points until an admin moves it.

-- ─── Membership ───────────────────────────────────────────────────────────
CREATE SCHEMA IF NOT EXISTS membership;

-- One sellable version of a membership tier in one market. A published
-- version never changes; a new price or bonus is a new version.
CREATE TABLE IF NOT EXISTS membership.plan_offers (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tier_code             text NOT NULL CHECK (tier_code ~ '^[a-z0-9_-]{2,40}$'),
    version               integer NOT NULL CHECK (version >= 1),
    country_id            uuid NOT NULL REFERENCES core.countries (id) ON DELETE RESTRICT,
    name                  text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    description           text CHECK (description IS NULL OR char_length(description) <= 600),
    currency              text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    price_minor           bigint NOT NULL CHECK (price_minor > 0),
    interval_unit         text NOT NULL CHECK (interval_unit IN ('day', 'week', 'month', 'year')),
    interval_count        integer NOT NULL CHECK (interval_count BETWEEN 1 AND 365),
    -- The extra award above ordinary gift points, never the combined total.
    member_bonus_points   bigint NOT NULL CHECK (member_bonus_points BETWEEN 0 AND 10000000),
    -- Shopping benefits shown before checkout, as short lines of text.
    gift_benefits         jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(gift_benefits) = 'array'),
    terms_version         text NOT NULL CHECK (char_length(terms_version) BETWEEN 1 AND 40),
    status                text NOT NULL DEFAULT 'draft'
                          CHECK (status IN ('draft', 'awaiting_approval', 'published', 'suspended', 'retired')),
    effective_from        timestamptz,
    created_by_admin_id   uuid,
    submitted_by_admin_id uuid,
    approved_by_admin_id  uuid,
    published_at          timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tier_code, country_id, version),
    -- Separation of duties: whoever submitted it cannot approve it.
    CONSTRAINT plan_offers_two_people CHECK (
        approved_by_admin_id IS NULL OR submitted_by_admin_id IS NULL
        OR approved_by_admin_id <> submitted_by_admin_id),
    CONSTRAINT plan_offers_published_fields CHECK (
        status NOT IN ('published', 'suspended', 'retired')
        OR (published_at IS NOT NULL AND approved_by_admin_id IS NOT NULL AND effective_from IS NOT NULL))
);

-- One published version of a tier per market at a time.
CREATE UNIQUE INDEX IF NOT EXISTS plan_offers_one_published
    ON membership.plan_offers (tier_code, country_id) WHERE status = 'published';

-- Once a version leaves draft, only its status (and who moved it) may change.
CREATE OR REPLACE FUNCTION membership.freeze_plan_offer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'draft' AND (
        NEW.tier_code, NEW.version, NEW.country_id, NEW.name, NEW.description, NEW.currency,
        NEW.price_minor, NEW.interval_unit, NEW.interval_count, NEW.member_bonus_points,
        NEW.gift_benefits, NEW.terms_version
    ) IS DISTINCT FROM (
        OLD.tier_code, OLD.version, OLD.country_id, OLD.name, OLD.description, OLD.currency,
        OLD.price_minor, OLD.interval_unit, OLD.interval_count, OLD.member_bonus_points,
        OLD.gift_benefits, OLD.terms_version
    ) THEN
        RAISE EXCEPTION 'membership.plan_offers: a submitted offer version is immutable; create a new version';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS plan_offers_freeze ON membership.plan_offers;
CREATE TRIGGER plan_offers_freeze
    BEFORE UPDATE ON membership.plan_offers
    FOR EACH ROW EXECUTE FUNCTION membership.freeze_plan_offer();

CREATE OR REPLACE FUNCTION membership.forbid_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'plan_offers' AND OLD.status = 'draft' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '%.% keeps its history; it cannot be deleted', TG_TABLE_SCHEMA, TG_TABLE_NAME;
END;
$$;

DROP TRIGGER IF EXISTS plan_offers_no_delete ON membership.plan_offers;
CREATE TRIGGER plan_offers_no_delete
    BEFORE DELETE ON membership.plan_offers
    FOR EACH ROW EXECUTE FUNCTION membership.forbid_delete();

-- A customer's membership with the payment provider. One canonical
-- entitlement at a time: at most one subscription that is not over.
CREATE TABLE IF NOT EXISTS membership.subscriptions (
    id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id               uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    offer_id                  uuid NOT NULL REFERENCES membership.plan_offers (id) ON DELETE RESTRICT,
    -- The version that applies from the next renewal, when it changes.
    scheduled_offer_id        uuid REFERENCES membership.plan_offers (id) ON DELETE RESTRICT,
    provider                  text NOT NULL CHECK (provider IN ('stripe', 'test')),
    provider_account          text NOT NULL DEFAULT 'platform',
    provider_subscription_id  text,
    provider_customer_id      text,
    checkout_session_id       text,
    checkout_url              text,
    status                    text NOT NULL DEFAULT 'incomplete'
                              CHECK (status IN ('incomplete', 'active', 'past_due', 'cancelling',
                                                'cancelled', 'expired', 'abandoned')),
    cancel_at_period_end      boolean NOT NULL DEFAULT false,
    current_period_end        timestamptz,
    cancel_requested_at       timestamptz,
    ended_at                  timestamptz,
    renewal_consent_at        timestamptz NOT NULL,
    idempotency_key           text NOT NULL,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),
    UNIQUE (customer_id, idempotency_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS subscriptions_provider_ref_uq
    ON membership.subscriptions (provider, provider_account, provider_subscription_id)
    WHERE provider_subscription_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS subscriptions_one_live
    ON membership.subscriptions (customer_id)
    WHERE status IN ('incomplete', 'active', 'past_due', 'cancelling');
CREATE INDEX IF NOT EXISTS subscriptions_checkout_idx
    ON membership.subscriptions (checkout_session_id) WHERE checkout_session_id IS NOT NULL;

-- A period the provider confirmed was paid for. Only these create a bonus
-- entitlement; a browser return or a pending invoice never does. Periods
-- are half-open: period_start <= t < period_end.
CREATE TABLE IF NOT EXISTS membership.paid_periods (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id           uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    subscription_id       uuid NOT NULL REFERENCES membership.subscriptions (id) ON DELETE RESTRICT,
    -- The offer version this period was sold under; its bonus quantity
    -- applies even after the tier changes.
    offer_id              uuid NOT NULL REFERENCES membership.plan_offers (id) ON DELETE RESTRICT,
    period_start          timestamptz NOT NULL,
    period_end            timestamptz NOT NULL,
    provider_invoice_id   text NOT NULL,
    provider_payment_id   text,
    amount_minor          bigint NOT NULL CHECK (amount_minor >= 0),
    currency              text NOT NULL,
    status                text NOT NULL DEFAULT 'paid'
                          CHECK (status IN ('paid', 'refunded', 'disputed', 'voided')),
    paid_at               timestamptz NOT NULL,
    status_changed_at     timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT paid_periods_order CHECK (period_end > period_start),
    UNIQUE (subscription_id, provider_invoice_id)
);

CREATE INDEX IF NOT EXISTS paid_periods_customer_idx
    ON membership.paid_periods (customer_id, period_start);

-- The one extra bonus of a paid period. Exactly one per customer and
-- period, created unawarded when the period is paid, awarded by the first
-- qualifying gift in it. A reversed slot stays reversed: it never resets
-- to unawarded, so a refunded first gift cannot buy a second bonus.
CREATE TABLE IF NOT EXISTS membership.bonus_slots (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id           uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    paid_period_id        uuid NOT NULL REFERENCES membership.paid_periods (id) ON DELETE RESTRICT,
    bonus_points          bigint NOT NULL CHECK (bonus_points >= 0),
    status                text NOT NULL DEFAULT 'unawarded'
                          CHECK (status IN ('unawarded', 'awarded', 'reversed', 'restored')),
    qualifying_gift_id    uuid,
    awarded_lot_id        uuid,
    awarded_at            timestamptz,
    status_reason         text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (customer_id, paid_period_id),
    CONSTRAINT bonus_slots_awarded_fields CHECK (
        status = 'unawarded' OR (qualifying_gift_id IS NOT NULL AND awarded_at IS NOT NULL))
);

-- ─── Reward policy and gift qualification ────────────────────────────────
-- How a gift earns ordinary competition points in one market. Versioned
-- like offers: an order keeps the version that was published when it was
-- paid for.
CREATE TABLE IF NOT EXISTS points.reward_policies (
    id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    country_id                uuid NOT NULL REFERENCES core.countries (id) ON DELETE RESTRICT,
    version                   integer NOT NULL CHECK (version >= 1),
    -- The qualifying unit: one paid gift order.
    qualifying_unit           text NOT NULL DEFAULT 'order' CHECK (qualifying_unit IN ('order')),
    -- Rewards are released only once every line is delivered.
    completion_status         text NOT NULL DEFAULT 'delivered' CHECK (completion_status IN ('delivered')),
    min_eligible_minor        bigint NOT NULL DEFAULT 0 CHECK (min_eligible_minor >= 0),
    exclude_free_gifts        boolean NOT NULL DEFAULT true,
    base_mode                 text NOT NULL CHECK (base_mode IN ('fixed', 'per_currency_unit')),
    base_fixed_points         bigint NOT NULL DEFAULT 0 CHECK (base_fixed_points >= 0),
    base_points_per_unit      integer NOT NULL DEFAULT 0 CHECK (base_points_per_unit BETWEEN 0 AND 10000),
    base_cap_points           bigint CHECK (base_cap_points IS NULL OR base_cap_points > 0),
    -- Points from this policy expire this many days after they are granted;
    -- null is no expiry. Disclosed before purchase.
    expiry_days               integer CHECK (expiry_days IS NULL OR expiry_days BETWEEN 1 AND 3650),
    status                    text NOT NULL DEFAULT 'draft'
                              CHECK (status IN ('draft', 'awaiting_approval', 'published', 'suspended', 'retired')),
    effective_from            timestamptz,
    created_by_admin_id       uuid,
    submitted_by_admin_id     uuid,
    approved_by_admin_id      uuid,
    published_at              timestamptz,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),
    UNIQUE (country_id, version),
    CONSTRAINT reward_policies_two_people CHECK (
        approved_by_admin_id IS NULL OR submitted_by_admin_id IS NULL
        OR approved_by_admin_id <> submitted_by_admin_id),
    CONSTRAINT reward_policies_formula CHECK (
        (base_mode = 'fixed' AND base_fixed_points > 0) OR
        (base_mode = 'per_currency_unit' AND base_points_per_unit > 0)),
    CONSTRAINT reward_policies_published_fields CHECK (
        status NOT IN ('published', 'suspended', 'retired')
        OR (published_at IS NOT NULL AND approved_by_admin_id IS NOT NULL AND effective_from IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS reward_policies_one_published
    ON points.reward_policies (country_id) WHERE status = 'published';

CREATE OR REPLACE FUNCTION points.freeze_reward_policy() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'draft' AND (
        NEW.country_id, NEW.version, NEW.qualifying_unit, NEW.completion_status, NEW.min_eligible_minor,
        NEW.exclude_free_gifts, NEW.base_mode, NEW.base_fixed_points, NEW.base_points_per_unit,
        NEW.base_cap_points, NEW.expiry_days
    ) IS DISTINCT FROM (
        OLD.country_id, OLD.version, OLD.qualifying_unit, OLD.completion_status, OLD.min_eligible_minor,
        OLD.exclude_free_gifts, OLD.base_mode, OLD.base_fixed_points, OLD.base_points_per_unit,
        OLD.base_cap_points, OLD.expiry_days
    ) THEN
        RAISE EXCEPTION 'points.reward_policies: a submitted policy version is immutable; create a new version';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS reward_policies_freeze ON points.reward_policies;
CREATE TRIGGER reward_policies_freeze
    BEFORE UPDATE ON points.reward_policies
    FOR EACH ROW EXECUTE FUNCTION points.freeze_reward_policy();

-- One qualifying gift: the order, the policy version and the anchor it was
-- paid under. The anchor (payment capture time) decides the paid period,
-- never delivery time or when an event happened to arrive.
CREATE TABLE IF NOT EXISTS points.gift_qualifications (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id              uuid NOT NULL UNIQUE REFERENCES marketplace.orders (id) ON DELETE RESTRICT,
    customer_id           uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    policy_id             uuid NOT NULL REFERENCES points.reward_policies (id) ON DELETE RESTRICT,
    payment_id            uuid REFERENCES finance.payments (id) ON DELETE RESTRICT,
    anchor_at             timestamptz NOT NULL,
    eligible_minor        bigint NOT NULL CHECK (eligible_minor >= 0),
    currency              text NOT NULL,
    base_points           bigint NOT NULL CHECK (base_points >= 0),
    status                text NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'awarded', 'cancelled', 'reversed', 'review')),
    base_lot_id           uuid,
    bonus_slot_id         uuid REFERENCES membership.bonus_slots (id) ON DELETE RESTRICT,
    completed_at          timestamptz,
    status_reason         text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS gift_qualifications_customer_idx
    ON points.gift_qualifications (customer_id, anchor_at);
CREATE INDEX IF NOT EXISTS gift_qualifications_open_idx
    ON points.gift_qualifications (status) WHERE status IN ('pending', 'awarded', 'review');

ALTER TABLE membership.bonus_slots
    DROP CONSTRAINT IF EXISTS bonus_slots_gift_fk,
    ADD CONSTRAINT bonus_slots_gift_fk FOREIGN KEY (qualifying_gift_id)
        REFERENCES points.gift_qualifications (id) ON DELETE RESTRICT;

-- ─── Competition point lots and their ledger ─────────────────────────────
-- A lot is one grant from one economic source. Its issued quantity is
-- always available + reserved + consumed + reversed + expired; moving
-- units between those buckets never changes it.
CREATE TABLE IF NOT EXISTS points.cp_lots (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id           uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    source                text NOT NULL CHECK (source IN ('gift', 'member_bonus', 'sponsored', 'compensation')),
    -- The qualification, bonus slot, allocation or approved case behind it.
    source_id             uuid NOT NULL,
    policy_id             uuid REFERENCES points.reward_policies (id) ON DELETE RESTRICT,
    original              bigint NOT NULL CHECK (original > 0),
    available             bigint NOT NULL CHECK (available >= 0),
    reserved              bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    consumed              bigint NOT NULL DEFAULT 0 CHECK (consumed >= 0),
    reversed              bigint NOT NULL DEFAULT 0 CHECK (reversed >= 0),
    expired               bigint NOT NULL DEFAULT 0 CHECK (expired >= 0),
    expires_at            timestamptz,
    -- Units released from a hold after the lot expired stay usable until
    -- this restoration window ends, so a failed start does not destroy them.
    grace_until           timestamptz,
    restrictions          jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(restrictions) = 'object'),
    -- A restoration is a new lot that names the one it restores.
    restores_lot_id       uuid REFERENCES points.cp_lots (id) ON DELETE RESTRICT,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT cp_lots_conserved CHECK (available + reserved + consumed + reversed + expired = original),
    -- One original grant per source: a second policy version, a retry or a
    -- duplicate event cannot mint the same gift or bonus twice.
    UNIQUE (source, source_id)
);

CREATE INDEX IF NOT EXISTS cp_lots_spendable_idx
    ON points.cp_lots (customer_id, expires_at NULLS LAST, created_at)
    WHERE available > 0;
CREATE INDEX IF NOT EXISTS cp_lots_expiring_idx
    ON points.cp_lots (expires_at) WHERE available > 0 AND expires_at IS NOT NULL;

ALTER TABLE points.gift_qualifications
    DROP CONSTRAINT IF EXISTS gift_qualifications_base_lot_fk,
    ADD CONSTRAINT gift_qualifications_base_lot_fk FOREIGN KEY (base_lot_id)
        REFERENCES points.cp_lots (id) ON DELETE RESTRICT;
ALTER TABLE membership.bonus_slots
    DROP CONSTRAINT IF EXISTS bonus_slots_lot_fk,
    ADD CONSTRAINT bonus_slots_lot_fk FOREIGN KEY (awarded_lot_id)
        REFERENCES points.cp_lots (id) ON DELETE RESTRICT;

-- One business operation on competition points. Its key is unique, so a
-- retried request, a duplicate webhook or a restarted worker returns the
-- result already recorded instead of doing it twice.
CREATE TABLE IF NOT EXISTS points.cp_transactions (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    operation_key         text NOT NULL UNIQUE,
    kind                  text NOT NULL CHECK (kind IN ('grant', 'reserve', 'capture', 'release',
                                                        'reverse', 'expire', 'restore')),
    customer_id           uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    actor_type            text NOT NULL CHECK (actor_type IN ('system', 'customer', 'admin')),
    actor_id              uuid,
    correlation_id        uuid,
    reason                text,
    created_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS cp_transactions_customer_idx
    ON points.cp_transactions (customer_id, created_at DESC);

-- Signed postings between a lot's buckets and the issuance account. Every
-- transaction's postings sum to zero.
CREATE TABLE IF NOT EXISTS points.cp_postings (
    id                    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id        uuid NOT NULL REFERENCES points.cp_transactions (id) ON DELETE RESTRICT,
    lot_id                uuid NOT NULL REFERENCES points.cp_lots (id) ON DELETE RESTRICT,
    bucket                text NOT NULL CHECK (bucket IN ('issuance', 'available', 'reserved', 'consumed',
                                                          'reversed', 'expired')),
    delta                 bigint NOT NULL CHECK (delta <> 0)
);

CREATE INDEX IF NOT EXISTS cp_postings_lot_idx ON points.cp_postings (lot_id);
CREATE INDEX IF NOT EXISTS cp_postings_tx_idx ON points.cp_postings (transaction_id);

CREATE OR REPLACE FUNCTION points.forbid_cp_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '%.% is append-only; post a compensating transaction instead', TG_TABLE_SCHEMA, TG_TABLE_NAME;
END;
$$;

DROP TRIGGER IF EXISTS cp_transactions_append_only ON points.cp_transactions;
CREATE TRIGGER cp_transactions_append_only
    BEFORE UPDATE OR DELETE ON points.cp_transactions
    FOR EACH ROW EXECUTE FUNCTION points.forbid_cp_mutation();
DROP TRIGGER IF EXISTS cp_postings_append_only ON points.cp_postings;
CREATE TRIGGER cp_postings_append_only
    BEFORE UPDATE OR DELETE ON points.cp_postings
    FOR EACH ROW EXECUTE FUNCTION points.forbid_cp_mutation();

-- Checked when the transaction commits, once all of its postings exist.
CREATE OR REPLACE FUNCTION points.check_cp_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    total bigint;
BEGIN
    SELECT coalesce(sum(delta), 0) INTO total
    FROM points.cp_postings WHERE transaction_id = NEW.transaction_id;
    IF total <> 0 THEN
        RAISE EXCEPTION 'points.cp_postings: transaction % does not balance (%)', NEW.transaction_id, total;
    END IF;
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS cp_postings_balanced ON points.cp_postings;
CREATE CONSTRAINT TRIGGER cp_postings_balanced
    AFTER INSERT ON points.cp_postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION points.check_cp_balanced();

-- Cases that need a person: a refund after the points were spent, a
-- restoration, a disputed membership payment. Resolved, never deleted.
CREATE TABLE IF NOT EXISTS points.cp_review_cases (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind                  text NOT NULL CHECK (kind IN ('refund_after_spend', 'bonus_restoration',
                                                        'membership_dispute', 'uncertain_start',
                                                        'reconciliation')),
    customer_id           uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    subject_type          text NOT NULL,
    subject_id            uuid NOT NULL,
    detail                jsonb NOT NULL DEFAULT '{}'::jsonb,
    status                text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    resolution            text,
    resolved_by_admin_id  uuid,
    resolved_at           timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (kind, subject_type, subject_id)
);

-- ─── Competition entries ─────────────────────────────────────────────────
-- A round chooses which unit it costs. Existing rounds stay on legacy
-- points; only rounds an admin moves to competition points use entries.
ALTER TABLE competition.competitions
    ADD COLUMN IF NOT EXISTS points_unit text NOT NULL DEFAULT 'legacy'
        CHECK (points_unit IN ('legacy', 'competition')),
    ADD COLUMN IF NOT EXISTS cp_allowed_sources text[] NOT NULL DEFAULT ARRAY['gift', 'member_bonus']::text[];

ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_cp_sources,
    ADD CONSTRAINT competitions_cp_sources CHECK (
        cp_allowed_sources <@ ARRAY['gift', 'member_bonus', 'sponsored', 'compensation']::text[]
        AND cardinality(cp_allowed_sources) >= 1);

-- A reservation of competition points for one attempt: reserved, then
-- captured once when the attempt starts or released once if it never does.
CREATE TABLE IF NOT EXISTS competition.cp_entries (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    competition_id        uuid NOT NULL REFERENCES competition.competitions (id) ON DELETE RESTRICT,
    customer_id           uuid NOT NULL REFERENCES customer.customers (id) ON DELETE RESTRICT,
    -- The rules version and cost the customer confirmed.
    rules_version         integer NOT NULL,
    cost                  bigint NOT NULL CHECK (cost > 0),
    channel               text NOT NULL CHECK (channel IN ('web', 'ios', 'android', 'event')),
    state                 text NOT NULL DEFAULT 'reserved'
                          CHECK (state IN ('reserved', 'captured', 'released', 'uncertain')),
    client_request_id     text NOT NULL CHECK (char_length(client_request_id) BETWEEN 8 AND 120),
    -- What the request asked for, so a reused key with a different request
    -- is refused instead of silently returning another entry.
    request_hash          text NOT NULL,
    attempt_id            uuid REFERENCES competition.competition_attempts (id) ON DELETE RESTRICT,
    reserve_tx_id         uuid NOT NULL REFERENCES points.cp_transactions (id) ON DELETE RESTRICT,
    settle_tx_id          uuid REFERENCES points.cp_transactions (id) ON DELETE RESTRICT,
    hold_expires_at       timestamptz NOT NULL,
    reserved_at           timestamptz NOT NULL DEFAULT now(),
    captured_at           timestamptz,
    released_at           timestamptz,
    release_reason        text,
    UNIQUE (customer_id, client_request_id),
    CONSTRAINT cp_entries_settled CHECK (
        (state = 'reserved' AND settle_tx_id IS NULL) OR
        (state = 'uncertain' AND settle_tx_id IS NULL) OR
        (state = 'captured' AND settle_tx_id IS NOT NULL AND captured_at IS NOT NULL AND attempt_id IS NOT NULL) OR
        (state = 'released' AND settle_tx_id IS NOT NULL AND released_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS cp_entries_open_idx
    ON competition.cp_entries (hold_expires_at) WHERE state = 'reserved';
CREATE INDEX IF NOT EXISTS cp_entries_round_idx
    ON competition.cp_entries (competition_id, customer_id, state);
CREATE UNIQUE INDEX IF NOT EXISTS cp_entries_attempt_uq
    ON competition.cp_entries (attempt_id) WHERE attempt_id IS NOT NULL;

-- Which lots an entry holds, and how much of each.
CREATE TABLE IF NOT EXISTS competition.cp_entry_lots (
    entry_id              uuid NOT NULL REFERENCES competition.cp_entries (id) ON DELETE RESTRICT,
    lot_id                uuid NOT NULL REFERENCES points.cp_lots (id) ON DELETE RESTRICT,
    quantity              bigint NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (entry_id, lot_id)
);

-- ─── Approvals and emergency stops ───────────────────────────────────────
-- Evidence that one operation is permitted in one market, channel and,
-- where it matters, provider. No row, an expired row or a revoked row
-- means unavailable: there is no global default.
CREATE TABLE IF NOT EXISTS core.capability_approvals (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    operation             text NOT NULL CHECK (operation IN (
                              'membership_sale', 'membership_renewal', 'gift_points_earning',
                              'competition_entry', 'winner_payout', 'seller_campaign_funding')),
    country_id            uuid NOT NULL REFERENCES core.countries (id) ON DELETE RESTRICT,
    -- 'any' for operations that run on the server whatever the channel.
    channel               text NOT NULL CHECK (channel IN ('any', 'web', 'ios', 'android', 'event')),
    provider              text,
    point_source          text CHECK (point_source IS NULL OR point_source IN ('gift', 'member_bonus', 'sponsored', 'compensation')),
    min_age               integer CHECK (min_age IS NULL OR min_age BETWEEN 13 AND 25),
    evidence_ref          text NOT NULL CHECK (char_length(trim(evidence_ref)) > 0),
    notes                 text,
    status                text NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'active', 'rejected', 'revoked')),
    effective_from        timestamptz NOT NULL,
    expires_at            timestamptz NOT NULL,
    requested_by_admin_id uuid NOT NULL,
    approved_by_admin_id  uuid,
    approved_at           timestamptz,
    revoked_by_admin_id   uuid,
    revoked_at            timestamptz,
    revoke_reason         text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT capability_approvals_window CHECK (expires_at > effective_from),
    CONSTRAINT capability_approvals_two_people CHECK (
        approved_by_admin_id IS NULL OR approved_by_admin_id <> requested_by_admin_id),
    CONSTRAINT capability_approvals_active_fields CHECK (
        status <> 'active' OR (approved_by_admin_id IS NOT NULL AND approved_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS capability_approvals_lookup_idx
    ON core.capability_approvals (operation, country_id, channel) WHERE status = 'active';

-- A scoped stop that takes effect at once. New affected actions are refused
-- on the server; refunds and existing obligations carry on.
CREATE TABLE IF NOT EXISTS core.emergency_stops (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope                 text NOT NULL CHECK (scope IN (
                              'membership_sales', 'membership_renewals', 'gift_points_earning',
                              'competition_entries', 'competition_starts', 'payouts',
                              'campaign_commitments')),
    -- Null stops it everywhere.
    country_id            uuid REFERENCES core.countries (id) ON DELETE RESTRICT,
    reason                text NOT NULL CHECK (char_length(trim(reason)) > 0),
    activated_by_admin_id uuid NOT NULL,
    activated_at          timestamptz NOT NULL DEFAULT now(),
    lifted_by_admin_id    uuid,
    lifted_at             timestamptz,
    lift_reason           text
);

CREATE INDEX IF NOT EXISTS emergency_stops_active_idx
    ON core.emergency_stops (scope) WHERE lifted_at IS NULL;
