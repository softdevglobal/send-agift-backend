-- The details a seller gives when they apply: business registration and tax,
-- the person applying, addresses, the shop they plan, how they deliver and
-- where they want to be paid. Admins read it to approve or reject the
-- account. Each section is a validated JSON document.
CREATE TABLE IF NOT EXISTS seller.seller_applications (
    seller_id             uuid PRIMARY KEY REFERENCES seller.sellers (id) ON DELETE CASCADE,
    business              jsonb NOT NULL,
    representative        jsonb NOT NULL,
    addresses             jsonb NOT NULL,
    shop                  jsonb NOT NULL,
    fulfilment            jsonb NOT NULL,
    payout                jsonb NOT NULL,
    consents              jsonb NOT NULL,
    review_reasons        text[] NOT NULL DEFAULT '{}',
    document_key          text,
    document_name         text,
    document_content_type text,
    document_uploaded_at  timestamptz,
    submitted_at          timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
