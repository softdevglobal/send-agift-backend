-- Who agreed to which terms, and when.
--
-- Registration used to keep, at most, a timestamp (sellers) or nothing at all
-- (customers). This keeps one row per assent with the exact version the
-- client showed, so a later change to the terms never rewrites what someone
-- agreed to. Rows are only ever added.
CREATE TABLE core.terms_acceptances (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_type  text NOT NULL CHECK (actor_type IN ('customer', 'seller')),
    actor_id    uuid NOT NULL,
    -- customer_terms covers the customer terms and privacy notice shown at
    -- sign-up; seller_terms the seller agreement.
    document    text NOT NULL CHECK (document IN ('customer_terms', 'seller_terms')),
    -- What the client said it showed, or 'unspecified' for clients too old
    -- to send one.
    version     text NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
    country_id  uuid REFERENCES core.countries(id),
    accepted_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX terms_acceptances_actor_idx
    ON core.terms_acceptances (actor_type, actor_id, accepted_at DESC);
