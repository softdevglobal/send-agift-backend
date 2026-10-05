-- Customers can sign in with Google or Facebook. One row links a provider
-- account (its stable subject id) to a customer, so a later sign-in finds the
-- same customer even if the email on the provider account changes.
CREATE TABLE IF NOT EXISTS customer.social_identities (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id  uuid NOT NULL REFERENCES customer.customers (id) ON DELETE CASCADE,
    provider     text NOT NULL CHECK (provider IN ('google', 'facebook')),
    subject      text NOT NULL,
    email        text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, subject)
);

CREATE INDEX IF NOT EXISTS social_identities_customer_idx
    ON customer.social_identities (customer_id);
