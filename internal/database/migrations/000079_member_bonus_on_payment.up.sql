-- When a membership package's bonus is credited.
--
--   * payment:    as soon as the provider confirms the period's payment.
--   * first_gift: after the first qualifying completed gift in the paid
--                 period (the handover's rule).
--
-- Either way it is credited once per paid period, as its own lot, and stays
-- until it is spent or its expiry: unused points carry into later periods.

ALTER TABLE membership.plan_offers
    ADD COLUMN IF NOT EXISTS bonus_trigger text NOT NULL DEFAULT 'payment'
        CHECK (bonus_trigger IN ('payment', 'first_gift'));

-- A bonus credited on payment has no qualifying gift.
ALTER TABLE membership.bonus_slots
    DROP CONSTRAINT IF EXISTS bonus_slots_awarded_fields,
    ADD CONSTRAINT bonus_slots_awarded_fields CHECK (status = 'unawarded' OR awarded_at IS NOT NULL);

-- The trigger is part of what was sold, so a published version keeps it.
CREATE OR REPLACE FUNCTION membership.freeze_plan_offer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'draft' AND (
        NEW.tier_code, NEW.version, NEW.country_id, NEW.name, NEW.description, NEW.currency,
        NEW.price_minor, NEW.interval_unit, NEW.interval_count, NEW.member_bonus_points,
        NEW.gift_benefits, NEW.terms_version, NEW.member_discount_bps, NEW.bonus_trigger
    ) IS DISTINCT FROM (
        OLD.tier_code, OLD.version, OLD.country_id, OLD.name, OLD.description, OLD.currency,
        OLD.price_minor, OLD.interval_unit, OLD.interval_count, OLD.member_bonus_points,
        OLD.gift_benefits, OLD.terms_version, OLD.member_discount_bps, OLD.bonus_trigger
    ) THEN
        RAISE EXCEPTION 'membership.plan_offers: a submitted offer version is immutable; create a new version';
    END IF;
    RETURN NEW;
END;
$$;
