-- Seller sign-up collects business, contact, shop and delivery details in one
-- request. Everything here is additive: new columns are nullable or have
-- defaults, so existing sellers, shops and queries are unchanged.

ALTER TABLE seller.sellers
    ADD COLUMN IF NOT EXISTS local_name             text,
    ADD COLUMN IF NOT EXISTS registration_status    text,
    ADD COLUMN IF NOT EXISTS registration_note      text,
    ADD COLUMN IF NOT EXISTS tax_status             text,
    ADD COLUMN IF NOT EXISTS contact_name           text,
    ADD COLUMN IF NOT EXISTS contact_role           text,
    ADD COLUMN IF NOT EXISTS contact_job_title      text,
    ADD COLUMN IF NOT EXISTS authority_confirmed_at timestamptz,
    ADD COLUMN IF NOT EXISTS terms_accepted_at      timestamptz,
    ADD COLUMN IF NOT EXISTS marketing_opt_in       boolean NOT NULL DEFAULT false;

ALTER TABLE seller.sellers
    DROP CONSTRAINT IF EXISTS sellers_registration_status_check;
ALTER TABLE seller.sellers
    ADD CONSTRAINT sellers_registration_status_check
    CHECK (registration_status IS NULL OR registration_status IN ('registered', 'pending', 'no_number'));

ALTER TABLE seller.sellers
    DROP CONSTRAINT IF EXISTS sellers_tax_status_check;
ALTER TABLE seller.sellers
    ADD CONSTRAINT sellers_tax_status_check
    CHECK (tax_status IS NULL OR tax_status IN ('registered', 'not_registered', 'unsure'));

ALTER TABLE seller.sellers
    DROP CONSTRAINT IF EXISTS sellers_contact_role_check;
ALTER TABLE seller.sellers
    ADD CONSTRAINT sellers_contact_role_check
    CHECK (contact_role IS NULL OR contact_role IN ('owner', 'director', 'authorised'));

-- Business identifiers (ABN, company number, trade licence ...). A business
-- can hold several. value_normalised is upper-case with spaces, dots and
-- hyphens removed, for duplicate checks during review.
CREATE TABLE IF NOT EXISTS seller.seller_identifiers (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id        uuid NOT NULL REFERENCES seller.sellers (id) ON DELETE CASCADE,
    country_id       uuid NOT NULL REFERENCES core.countries (id),
    identifier_type  text NOT NULL,
    value            text NOT NULL,
    value_normalised text NOT NULL,
    authority        text,
    jurisdiction     text,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_seller_identifiers_seller_id
    ON seller.seller_identifiers (seller_id);
CREATE INDEX IF NOT EXISTS idx_seller_identifiers_lookup
    ON seller.seller_identifiers (country_id, identifier_type, value_normalised);

-- Business tax registrations. One per country / jurisdiction / scheme.
CREATE TABLE IF NOT EXISTS seller.seller_tax_registrations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id    uuid NOT NULL REFERENCES seller.sellers (id) ON DELETE CASCADE,
    country_id   uuid NOT NULL REFERENCES core.countries (id),
    jurisdiction text,
    scheme       text NOT NULL,
    tax_number   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_seller_tax_registrations_seller_id
    ON seller.seller_tax_registrations (seller_id);

-- The registered business address is a private record. It is never used as a
-- delivery origin: only pickup / both addresses are linked to a shop.
ALTER TABLE seller.seller_addresses
    DROP CONSTRAINT IF EXISTS seller_addresses_address_type_check;
ALTER TABLE seller.seller_addresses
    ADD CONSTRAINT seller_addresses_address_type_check
    CHECK (address_type IN ('pickup', 'return', 'both', 'registered'));

ALTER TABLE seller.shops
    ADD COLUMN IF NOT EXISTS website        text,
    ADD COLUMN IF NOT EXISTS support_email  text,
    ADD COLUMN IF NOT EXISTS returns_policy text,
    ADD COLUMN IF NOT EXISTS categories     text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS gift_options   text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS working_days   text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS pickup_enabled boolean NOT NULL DEFAULT false;

ALTER TABLE seller.shops
    DROP CONSTRAINT IF EXISTS shops_working_days_check;
ALTER TABLE seller.shops
    ADD CONSTRAINT shops_working_days_check
    CHECK (working_days <@ ARRAY['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun']::text[]);
