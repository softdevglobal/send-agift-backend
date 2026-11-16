-- Points are retired. Customers now earn game entries by buying products a
-- seller promotes with an advertising package, and spend them to play skill
-- games. Games and entry earning are off until an admin switches them on
-- for a country, a state and a game.
--
-- Nothing a customer or seller held is deleted: the whole points schema is
-- kept, renamed to legacy_points, with its balances frozen until the owner
-- decides whether to refund or convert them.

-- ─── 1. Archive the points system ─────────────────────────────────────────

ALTER SCHEMA points RENAME TO legacy_points;
ALTER TABLE competition.cp_entries SET SCHEMA legacy_points;
ALTER TABLE competition.cp_entry_lots SET SCHEMA legacy_points;
ALTER TABLE membership.bonus_slots SET SCHEMA legacy_points;

COMMENT ON SCHEMA legacy_points IS
    'Retired points system (archived by 000082). Read only: balances are frozen until the owner approves a refund or conversion.';

CREATE FUNCTION legacy_points.forbid_balance_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'legacy_points.%: points are retired; balances are frozen', TG_TABLE_NAME;
END;
$$;

CREATE TRIGGER points_accounts_frozen BEFORE INSERT OR UPDATE ON legacy_points.points_accounts
    FOR EACH STATEMENT EXECUTE FUNCTION legacy_points.forbid_balance_change();
CREATE TRIGGER seller_points_accounts_frozen BEFORE INSERT OR UPDATE ON legacy_points.seller_points_accounts
    FOR EACH STATEMENT EXECUTE FUNCTION legacy_points.forbid_balance_change();

-- Points columns on live tables keep their data under a legacy_ name.
ALTER TABLE marketplace.order_items RENAME COLUMN reward_points TO legacy_reward_points;
ALTER TABLE marketplace.order_items RENAME COLUMN reward_points_per_unit TO legacy_reward_points_per_unit;
ALTER TABLE marketplace.order_items RENAME COLUMN reward_status TO legacy_reward_status;
ALTER TABLE marketplace.orders RENAME COLUMN gift_points TO legacy_gift_points;
ALTER TABLE marketplace.orders RENAME COLUMN gift_points_status TO legacy_gift_points_status;
ALTER TABLE marketplace.orders RENAME COLUMN gift_points_recipient_id TO legacy_gift_points_recipient_id;
ALTER TABLE seller.products RENAME COLUMN reward_points TO legacy_reward_points;
ALTER TABLE seller.products RENAME COLUMN points_display_enabled TO legacy_points_display_enabled;
ALTER TABLE competition.competitions RENAME COLUMN points_per_attempt TO legacy_points_per_attempt;
ALTER TABLE competition.competitions RENAME COLUMN prize_points TO legacy_prize_points;
ALTER TABLE competition.competitions RENAME COLUMN points_unit TO legacy_points_unit;
ALTER TABLE competition.competitions RENAME COLUMN cp_allowed_sources TO legacy_cp_allowed_sources;
ALTER TABLE competition.competition_attempts RENAME COLUMN points_spent TO legacy_points_spent;
ALTER TABLE competition.competition_attempts RENAME COLUMN points_ledger_id TO legacy_points_ledger_id;
ALTER TABLE competition.games RENAME COLUMN play_cost_points TO legacy_play_cost_points;
ALTER TABLE finance.refunds RENAME COLUMN points_reversal_status TO entries_reversal_status;
ALTER TABLE core.country_capabilities RENAME COLUMN points_earning_enabled TO entries_earning_enabled;
ALTER TABLE core.country_capabilities RENAME COLUMN points_usage_enabled TO entries_usage_enabled;

-- Membership no longer carries a points bonus. Published offers are
-- immutable, so the column stays; new offers default to none.
ALTER TABLE membership.plan_offers ALTER COLUMN member_bonus_points SET DEFAULT 0;

-- ─── 2. States and regions ────────────────────────────────────────────────

CREATE TABLE core.states (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    country_id uuid NOT NULL REFERENCES core.countries(id) ON DELETE RESTRICT,
    code text NOT NULL CHECK (code ~ '^[A-Z0-9-]{1,10}$'),
    name text NOT NULL CHECK (char_length(trim(name)) BETWEEN 1 AND 80),
    is_active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (country_id, code)
);
CREATE INDEX states_country_idx ON core.states (country_id, name);

-- A starting list for the countries most likely to need one. Admins manage
-- the list from here on.
INSERT INTO core.states (country_id, code, name)
SELECT c.id, s.code, s.name
FROM core.countries c
JOIN (VALUES
    ('AU','NSW','New South Wales'), ('AU','VIC','Victoria'), ('AU','QLD','Queensland'),
    ('AU','WA','Western Australia'), ('AU','SA','South Australia'), ('AU','TAS','Tasmania'),
    ('AU','ACT','Australian Capital Territory'), ('AU','NT','Northern Territory'),
    ('LK','WP','Western Province'), ('LK','CP','Central Province'), ('LK','SP','Southern Province'),
    ('LK','NP','Northern Province'), ('LK','EP','Eastern Province'), ('LK','NWP','North Western Province'),
    ('LK','NCP','North Central Province'), ('LK','UP','Uva Province'), ('LK','SGP','Sabaragamuwa Province'),
    ('CA','AB','Alberta'), ('CA','BC','British Columbia'), ('CA','MB','Manitoba'), ('CA','NB','New Brunswick'),
    ('CA','NL','Newfoundland and Labrador'), ('CA','NS','Nova Scotia'), ('CA','NT','Northwest Territories'),
    ('CA','NU','Nunavut'), ('CA','ON','Ontario'), ('CA','PE','Prince Edward Island'), ('CA','QC','Quebec'),
    ('CA','SK','Saskatchewan'), ('CA','YT','Yukon'),
    ('US','AL','Alabama'), ('US','AK','Alaska'), ('US','AZ','Arizona'), ('US','AR','Arkansas'),
    ('US','CA','California'), ('US','CO','Colorado'), ('US','CT','Connecticut'), ('US','DE','Delaware'),
    ('US','DC','District of Columbia'), ('US','FL','Florida'), ('US','GA','Georgia'), ('US','HI','Hawaii'),
    ('US','ID','Idaho'), ('US','IL','Illinois'), ('US','IN','Indiana'), ('US','IA','Iowa'),
    ('US','KS','Kansas'), ('US','KY','Kentucky'), ('US','LA','Louisiana'), ('US','ME','Maine'),
    ('US','MD','Maryland'), ('US','MA','Massachusetts'), ('US','MI','Michigan'), ('US','MN','Minnesota'),
    ('US','MS','Mississippi'), ('US','MO','Missouri'), ('US','MT','Montana'), ('US','NE','Nebraska'),
    ('US','NV','Nevada'), ('US','NH','New Hampshire'), ('US','NJ','New Jersey'), ('US','NM','New Mexico'),
    ('US','NY','New York'), ('US','NC','North Carolina'), ('US','ND','North Dakota'), ('US','OH','Ohio'),
    ('US','OK','Oklahoma'), ('US','OR','Oregon'), ('US','PA','Pennsylvania'), ('US','RI','Rhode Island'),
    ('US','SC','South Carolina'), ('US','SD','South Dakota'), ('US','TN','Tennessee'), ('US','TX','Texas'),
    ('US','UT','Utah'), ('US','VT','Vermont'), ('US','VA','Virginia'), ('US','WA','Washington'),
    ('US','WV','West Virginia'), ('US','WI','Wisconsin'), ('US','WY','Wyoming')
) AS s(iso, code, name) ON upper(c.iso_code) = s.iso
ON CONFLICT DO NOTHING;

-- The customer's own state: asked at sign-up, never guessed. state_set_at
-- drives the cooldown on changing it.
ALTER TABLE customer.customers
    ADD COLUMN state_id uuid REFERENCES core.states(id) ON DELETE RESTRICT,
    ADD COLUMN state_set_at timestamptz,
    ADD COLUMN state_source text CHECK (state_source IN ('declared', 'address', 'admin'));
CREATE INDEX customers_state_idx ON customer.customers (state_id);

-- ─── 3. Where games and entries are switched on ───────────────────────────

CREATE SCHEMA IF NOT EXISTS entries;

-- One row: the whole programme. Off until an admin turns it on.
CREATE TABLE entries.program (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    enabled boolean NOT NULL DEFAULT false,
    stopped_at timestamptz,
    stopped_reason text,
    updated_by_admin_id uuid,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO entries.program DEFAULT VALUES;

-- A country, or one state of it. A state never inherits from its country:
-- no row, or a row that is off, means unavailable. state_id is null only
-- for a country that has no states.
CREATE TABLE entries.jurisdictions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    country_id uuid NOT NULL REFERENCES core.countries(id) ON DELETE RESTRICT,
    state_id uuid REFERENCES core.states(id) ON DELETE RESTRICT,
    earning_enabled boolean NOT NULL DEFAULT false,
    play_enabled boolean NOT NULL DEFAULT false,
    chance_games_enabled boolean NOT NULL DEFAULT false,
    min_age integer NOT NULL DEFAULT 18 CHECK (min_age BETWEEN 13 AND 25),
    official_rules text CHECK (official_rules IS NULL OR char_length(official_rules) <= 20000),
    -- Most entries one customer can earn in a day, and from one product in a
    -- day. Null is no cap.
    customer_daily_cap integer CHECK (customer_daily_cap IS NULL OR customer_daily_cap > 0),
    product_daily_cap integer CHECK (product_daily_cap IS NULL OR product_daily_cap > 0),
    -- The no-purchase route, where counsel requires one. 0 is off.
    free_entries_per_day integer NOT NULL DEFAULT 0 CHECK (free_entries_per_day BETWEEN 0 AND 100),
    stopped_at timestamptz,
    stopped_reason text,
    updated_by_admin_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT jurisdictions_rules_before_on CHECK (
        NOT (earning_enabled OR play_enabled)
        OR (official_rules IS NOT NULL AND char_length(trim(official_rules)) > 0)
    )
);
CREATE UNIQUE INDEX jurisdictions_place_uq ON entries.jurisdictions
    (country_id, coalesce(state_id, '00000000-0000-0000-0000-000000000000'::uuid));

CREATE TABLE entries.jurisdiction_games (
    jurisdiction_id uuid NOT NULL REFERENCES entries.jurisdictions(id) ON DELETE CASCADE,
    game_id uuid NOT NULL REFERENCES competition.games(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (jurisdiction_id, game_id)
);

-- ─── 4. Advertising packages ──────────────────────────────────────────────

CREATE SCHEMA IF NOT EXISTS ads;

-- A package version, for one country. Once published it never changes:
-- editing makes a new version under the same package_key, so a seller keeps
-- the terms they bought. level_entries[1] is the top priority level.
CREATE TABLE ads.packages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    package_key uuid NOT NULL DEFAULT gen_random_uuid(),
    version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
    country_id uuid NOT NULL REFERENCES core.countries(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (char_length(trim(name)) BETWEEN 1 AND 80),
    description text CHECK (description IS NULL OR char_length(description) <= 600),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    price_minor bigint NOT NULL CHECK (price_minor > 0),
    product_slots integer NOT NULL CHECK (product_slots BETWEEN 1 AND 20),
    level_entries integer[] NOT NULL,
    entries_pool integer NOT NULL CHECK (entries_pool > 0),
    validity_days integer NOT NULL CHECK (validity_days BETWEEN 1 AND 365),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'retired')),
    published_at timestamptz,
    retired_at timestamptz,
    created_by_admin_id uuid,
    updated_by_admin_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (package_key, version),
    CONSTRAINT packages_levels CHECK (
        cardinality(level_entries) = product_slots
        AND 1 <= ALL (level_entries) AND 1000 >= ALL (level_entries)
    )
);
CREATE UNIQUE INDEX packages_one_published ON ads.packages (package_key) WHERE status = 'published';
CREATE INDEX packages_country_idx ON ads.packages (country_id, status);

CREATE FUNCTION ads.freeze_package() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'draft' AND (
        NEW.package_key, NEW.version, NEW.country_id, NEW.name, NEW.description, NEW.currency,
        NEW.price_minor, NEW.product_slots, NEW.level_entries, NEW.entries_pool, NEW.validity_days
    ) IS DISTINCT FROM (
        OLD.package_key, OLD.version, OLD.country_id, OLD.name, OLD.description, OLD.currency,
        OLD.price_minor, OLD.product_slots, OLD.level_entries, OLD.entries_pool, OLD.validity_days
    ) THEN
        RAISE EXCEPTION 'ads.packages: a published package version is immutable; create a new version';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER packages_freeze BEFORE UPDATE ON ads.packages
    FOR EACH ROW EXECUTE FUNCTION ads.freeze_package();

-- A seller's purchase of a package. It copies the terms it was bought on.
-- entries_used is what customers have been granted; entries_held is what
-- unpaid checkouts have been promised. Together they never pass the pool.
CREATE TABLE ads.purchases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id uuid NOT NULL REFERENCES seller.sellers(id) ON DELETE CASCADE,
    package_id uuid NOT NULL REFERENCES ads.packages(id) ON DELETE RESTRICT,
    package_name text NOT NULL,
    country_id uuid NOT NULL REFERENCES core.countries(id) ON DELETE RESTRICT,
    currency text NOT NULL,
    price_minor bigint NOT NULL CHECK (price_minor > 0),
    product_slots integer NOT NULL,
    level_entries integer[] NOT NULL,
    entries_pool integer NOT NULL CHECK (entries_pool > 0),
    validity_days integer NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'active', 'exhausted', 'expired', 'cancelled', 'failed', 'refunded')),
    entries_used integer NOT NULL DEFAULT 0 CHECK (entries_used >= 0),
    entries_held integer NOT NULL DEFAULT 0 CHECK (entries_held >= 0),
    provider text NOT NULL,
    provider_reference text,
    checkout_url text,
    receipt_url text,
    paid_amount_minor bigint,
    failure_reason text,
    idempotency_key text NOT NULL,
    last_reminder_threshold integer NOT NULL DEFAULT 0,
    paid_at timestamptz,
    expires_at timestamptz,
    ended_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (seller_id, idempotency_key),
    CONSTRAINT purchases_within_pool CHECK (entries_used + entries_held <= entries_pool)
);
CREATE UNIQUE INDEX purchases_provider_ref_uq ON ads.purchases (provider, provider_reference)
    WHERE provider_reference IS NOT NULL;
CREATE INDEX purchases_seller_idx ON ads.purchases (seller_id, created_at DESC);
CREATE INDEX purchases_active_idx ON ads.purchases (expires_at) WHERE status = 'active';

-- A product a seller promotes in one priority level of a purchase. Rows are
-- never deleted: taking a product off the list sets removed_at, so what it
-- earned stays on record.
CREATE TABLE ads.promotions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_id uuid NOT NULL REFERENCES ads.purchases(id) ON DELETE CASCADE,
    seller_id uuid NOT NULL REFERENCES seller.sellers(id) ON DELETE CASCADE,
    product_id uuid NOT NULL REFERENCES seller.products(id) ON DELETE CASCADE,
    level integer NOT NULL CHECK (level >= 1),
    entries_per_purchase integer NOT NULL CHECK (entries_per_purchase >= 1),
    entries_granted integer NOT NULL DEFAULT 0 CHECK (entries_granted >= 0),
    orders_count integer NOT NULL DEFAULT 0 CHECK (orders_count >= 0),
    removed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX promotions_level_uq ON ads.promotions (purchase_id, level) WHERE removed_at IS NULL;
CREATE UNIQUE INDEX promotions_product_uq ON ads.promotions (product_id) WHERE removed_at IS NULL;
CREATE INDEX promotions_purchase_idx ON ads.promotions (purchase_id);

-- Budget reminders to the seller: one automatic notice per threshold per
-- purchase, plus any an admin sends again.
CREATE TABLE ads.reminders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_id uuid NOT NULL REFERENCES ads.purchases(id) ON DELETE CASCADE,
    seller_id uuid NOT NULL REFERENCES seller.sellers(id) ON DELETE CASCADE,
    threshold integer NOT NULL CHECK (threshold IN (80, 85, 90, 95, 100)),
    usage_percent integer NOT NULL,
    kind text NOT NULL DEFAULT 'auto' CHECK (kind IN ('auto', 'resend')),
    sent_by_admin_id uuid,
    read_at timestamptz,
    emailed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reminders_unsent_idx ON ads.reminders (created_at) WHERE emailed_at IS NULL;
CREATE UNIQUE INDEX reminders_once_uq ON ads.reminders (purchase_id, threshold) WHERE kind = 'auto';
CREATE INDEX reminders_seller_idx ON ads.reminders (seller_id, created_at DESC);

-- ─── 5. Customer game entries ─────────────────────────────────────────────

CREATE TABLE entries.accounts (
    customer_id uuid PRIMARY KEY REFERENCES customer.customers(id) ON DELETE CASCADE,
    balance bigint NOT NULL DEFAULT 0 CHECK (balance >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE entries.ledger (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customer.customers(id) ON DELETE CASCADE,
    entry_type text NOT NULL CHECK (entry_type IN (
        'purchase_grant', 'purchase_reversal', 'play_debit', 'play_refund',
        'free_entry', 'admin_grant', 'admin_deduction')),
    delta bigint NOT NULL CHECK (delta <> 0),
    balance_after bigint NOT NULL CHECK (balance_after >= 0),
    order_id uuid,
    order_item_id uuid,
    ad_purchase_id uuid,
    promotion_id uuid,
    competition_id uuid,
    attempt_id uuid,
    game_session_id uuid,
    idempotency_key text NOT NULL UNIQUE,
    reason text,
    actor_type text NOT NULL DEFAULT 'system',
    actor_id uuid,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ledger_customer_idx ON entries.ledger (customer_id, created_at DESC);
CREATE INDEX ledger_competition_idx ON entries.ledger (competition_id) WHERE competition_id IS NOT NULL;
CREATE INDEX ledger_order_item_idx ON entries.ledger (order_item_id) WHERE order_item_id IS NOT NULL;

CREATE FUNCTION entries.forbid_ledger_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'entries.ledger is append only';
END;
$$;
CREATE TRIGGER ledger_append_only BEFORE UPDATE ON entries.ledger
    FOR EACH ROW EXECUTE FUNCTION entries.forbid_ledger_update();

-- Entries that could not be taken back after a refund because the customer
-- had already used them. Recorded for review; never a debt or a negative
-- balance.
CREATE TABLE entries.shortfalls (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid NOT NULL REFERENCES customer.customers(id) ON DELETE CASCADE,
    order_id uuid NOT NULL,
    order_item_id uuid NOT NULL UNIQUE,
    entries_granted integer NOT NULL,
    entries_recovered integer NOT NULL,
    entries_short integer NOT NULL CHECK (entries_short > 0),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    resolution text,
    resolved_by_admin_id uuid,
    resolved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX shortfalls_open_idx ON entries.shortfalls (created_at) WHERE status = 'open';

-- Entries riding on an order line: promised (reserved from the seller's
-- package) at checkout, granted when the payment is confirmed.
ALTER TABLE marketplace.order_items
    ADD COLUMN game_entries integer NOT NULL DEFAULT 0 CHECK (game_entries >= 0),
    ADD COLUMN entries_status text NOT NULL DEFAULT 'none'
        CHECK (entries_status IN ('none', 'reserved', 'granted', 'released', 'reversed')),
    ADD COLUMN promotion_id uuid REFERENCES ads.promotions(id) ON DELETE SET NULL,
    ADD CONSTRAINT order_items_entries_has CHECK (entries_status = 'none' OR game_entries > 0);
CREATE INDEX order_items_entries_open_idx ON marketplace.order_items (entries_status)
    WHERE entries_status IN ('reserved', 'granted');

-- ─── 6. Games and competitions cost entries ───────────────────────────────

-- What one play costs, set per game by an admin. Old point prices are not
-- carried over: every game that had a price starts at one entry.
ALTER TABLE competition.games
    ADD COLUMN entry_cost integer NOT NULL DEFAULT 1 CHECK (entry_cost BETWEEN 0 AND 1000);
UPDATE competition.games SET entry_cost = CASE WHEN legacy_play_cost_points > 0 THEN 1 ELSE 0 END;

ALTER TABLE competition.competitions
    ADD COLUMN entries_per_attempt integer NOT NULL DEFAULT 0 CHECK (entries_per_attempt BETWEEN 0 AND 1000);
UPDATE competition.competitions SET entries_per_attempt = CASE WHEN legacy_points_per_attempt > 0 THEN 1 ELSE 0 END;

ALTER TABLE competition.competition_attempts
    ADD COLUMN entries_spent integer NOT NULL DEFAULT 0 CHECK (entries_spent >= 0),
    ADD COLUMN entry_ledger_id uuid;

-- The states a competition is open to. Empty means every state of its
-- countries (and is all a country without states can have).
CREATE TABLE competition.competition_states (
    competition_id uuid NOT NULL REFERENCES competition.competitions(id) ON DELETE CASCADE,
    state_id uuid NOT NULL REFERENCES core.states(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (competition_id, state_id)
);
CREATE INDEX competition_states_state_idx ON competition.competition_states (state_id);
