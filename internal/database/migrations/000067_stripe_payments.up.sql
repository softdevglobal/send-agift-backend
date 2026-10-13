-- Stripe, laid out as the database structure document describes: customers
-- pay the platform (finance.payments), refunds are recorded
-- (finance.refunds), delivered order lines are settled to the seller's
-- Connect account (finance.payouts, with finance.payment_splits holding the
-- seller / platform split), and the Connect account itself lives in
-- seller.seller_payout_accounts. Webhook events are de-duplicated through
-- core.idempotency_keys (scope 'webhook').

-- 7.2 seller.seller_payout_accounts
CREATE TABLE IF NOT EXISTS seller.seller_payout_accounts (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id           uuid NOT NULL REFERENCES seller.sellers (id) ON DELETE CASCADE,
    provider            text NOT NULL DEFAULT 'stripe',
    provider_account_id text NOT NULL,
    country_id          uuid NOT NULL REFERENCES core.countries (id),
    capabilities_status text NOT NULL DEFAULT 'pending'
                        CHECK (capabilities_status IN ('pending', 'enabled', 'restricted', 'disabled')),
    payouts_enabled     boolean NOT NULL DEFAULT false,
    charges_enabled     boolean NOT NULL DEFAULT false,
    -- Not in the document: whether the seller has finished the hosted form.
    details_submitted   boolean NOT NULL DEFAULT false,
    requirements_due    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_account_id),
    UNIQUE (seller_id, provider)
);

-- 9.1 finance.payments
CREATE TABLE IF NOT EXISTS finance.payments (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id            uuid NOT NULL REFERENCES marketplace.orders (id) ON DELETE CASCADE,
    customer_id         uuid NOT NULL REFERENCES customer.customers (id),
    provider            text NOT NULL,
    provider_payment_id text NOT NULL,
    amount              integer NOT NULL CHECK (amount > 0),
    currency            text NOT NULL,
    status              text NOT NULL DEFAULT 'requires_payment'
                        CHECK (status IN ('requires_payment', 'processing', 'succeeded',
                                          'failed', 'cancelled', 'refunded')),
    idempotency_key     text NOT NULL UNIQUE,
    -- Not in the document: when the provider confirmed the money.
    paid_at             timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_payment_id)
);

CREATE INDEX IF NOT EXISTS payments_order_idx ON finance.payments (order_id);

-- 9.2 finance.payment_splits
CREATE TABLE IF NOT EXISTS finance.payment_splits (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id uuid NOT NULL REFERENCES finance.payments (id) ON DELETE CASCADE,
    seller_id  uuid REFERENCES seller.sellers (id),
    split_type text NOT NULL
               CHECK (split_type IN ('seller_gross', 'platform_commission',
                                     'processing_fee', 'courier_margin', 'tax')),
    amount     integer NOT NULL,
    currency   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS payment_splits_payment_idx ON finance.payment_splits (payment_id);

-- 9.3 finance.refunds
CREATE TABLE IF NOT EXISTS finance.refunds (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id               uuid NOT NULL REFERENCES marketplace.orders (id),
    payment_id             uuid NOT NULL REFERENCES finance.payments (id),
    amount                 integer NOT NULL CHECK (amount > 0),
    reason                 text,
    status                 text NOT NULL DEFAULT 'requested'
                           CHECK (status IN ('requested', 'approved', 'processing',
                                             'succeeded', 'rejected')),
    points_reversal_status text NOT NULL DEFAULT 'not_required'
                           CHECK (points_reversal_status IN ('not_required', 'pending',
                                                             'completed', 'failed')),
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS refunds_payment_idx ON finance.refunds (payment_id);

-- 9.4 finance.payouts: here, one settlement to a seller's Connect account for
-- one delivered order line. The unique order_item_id is what stops a line
-- being paid out twice.
CREATE TABLE IF NOT EXISTS finance.payouts (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id             uuid NOT NULL REFERENCES seller.sellers (id),
    provider              text NOT NULL,
    provider_payout_id    text,
    amount                integer NOT NULL CHECK (amount > 0),
    currency              text NOT NULL,
    status                text NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'in_transit', 'paid', 'failed', 'held')),
    hold_reason           text,
    expected_arrival_date date,
    -- Not in the document: which order line this settles, and its split.
    order_item_id         uuid UNIQUE REFERENCES marketplace.order_items (id),
    order_id              uuid REFERENCES marketplace.orders (id),
    gross_amount          integer,
    fee_amount            integer NOT NULL DEFAULT 0 CHECK (fee_amount >= 0),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS payouts_provider_payout_uq
    ON finance.payouts (provider, provider_payout_id) WHERE provider_payout_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS payouts_seller_idx ON finance.payouts (seller_id, created_at DESC);
