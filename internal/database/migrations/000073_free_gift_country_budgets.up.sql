-- Free gift marketing budget by country. An admin sets one amount for a
-- country and it is applied to every seller registered there; each seller's
-- own program (promo.free_gift_programs) still tracks what they hold and spend.
CREATE TABLE IF NOT EXISTS promo.free_gift_country_budgets (
    country_id  uuid PRIMARY KEY REFERENCES core.countries (id) ON DELETE CASCADE,
    currency    text NOT NULL,
    budget_cap  bigint NOT NULL CHECK (budget_cap > 0),   -- minor units, per seller
    status      text NOT NULL DEFAULT 'paused' CHECK (status IN ('active', 'paused')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
