-- Member product discount.
--
-- A paid membership can give members a percentage off the gift price. The
-- discount is funded by SendAGift out of its own commission, never by the
-- seller:
--   * the line price (order_items.total_amount) stays the seller's list
--     price, so what a seller earns and the commission taken on it do not
--     change;
--   * the customer pays the list price less the discount, which is
--     orders.total_amount = subtotal + delivery - member discount;
--   * the percentage is capped by the platform's commission when the offer
--     is saved, and again when an order is priced.

ALTER TABLE membership.plan_offers
    ADD COLUMN IF NOT EXISTS member_discount_bps integer NOT NULL DEFAULT 0
        CHECK (member_discount_bps BETWEEN 0 AND 5000);

-- A published offer version is immutable; its discount is part of what was
-- sold, so the freeze check covers it too.
CREATE OR REPLACE FUNCTION membership.freeze_plan_offer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'draft' AND (
        NEW.tier_code, NEW.version, NEW.country_id, NEW.name, NEW.description, NEW.currency,
        NEW.price_minor, NEW.interval_unit, NEW.interval_count, NEW.member_bonus_points,
        NEW.gift_benefits, NEW.terms_version, NEW.member_discount_bps
    ) IS DISTINCT FROM (
        OLD.tier_code, OLD.version, OLD.country_id, OLD.name, OLD.description, OLD.currency,
        OLD.price_minor, OLD.interval_unit, OLD.interval_count, OLD.member_bonus_points,
        OLD.gift_benefits, OLD.terms_version, OLD.member_discount_bps
    ) THEN
        RAISE EXCEPTION 'membership.plan_offers: a submitted offer version is immutable; create a new version';
    END IF;
    RETURN NEW;
END;
$$;

-- What a member saved on an order, kept apart from the prices so a seller's
-- earnings never depend on it. The paid period and percentage are the ones
-- the discount was given under.
ALTER TABLE marketplace.orders
    ADD COLUMN IF NOT EXISTS member_discount_amount integer NOT NULL DEFAULT 0
        CHECK (member_discount_amount >= 0),
    ADD COLUMN IF NOT EXISTS member_discount_bps integer NOT NULL DEFAULT 0
        CHECK (member_discount_bps >= 0),
    ADD COLUMN IF NOT EXISTS member_paid_period_id uuid
        REFERENCES membership.paid_periods (id) ON DELETE RESTRICT;

ALTER TABLE marketplace.order_items
    ADD COLUMN IF NOT EXISTS member_discount_amount integer NOT NULL DEFAULT 0
        CHECK (member_discount_amount >= 0);

-- A discount only ever applies to the items, so it can never exceed them.
ALTER TABLE marketplace.orders
    DROP CONSTRAINT IF EXISTS orders_member_discount_within_subtotal,
    ADD CONSTRAINT orders_member_discount_within_subtotal
        CHECK (member_discount_amount <= subtotal_amount);
