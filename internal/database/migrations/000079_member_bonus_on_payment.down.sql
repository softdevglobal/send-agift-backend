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

-- Slots credited on payment have no gift; the original check is restored
-- for new rows only.
ALTER TABLE membership.bonus_slots
    DROP CONSTRAINT IF EXISTS bonus_slots_awarded_fields,
    ADD CONSTRAINT bonus_slots_awarded_fields CHECK (
        status = 'unawarded' OR (qualifying_gift_id IS NOT NULL AND awarded_at IS NOT NULL)) NOT VALID;

ALTER TABLE membership.plan_offers DROP COLUMN IF EXISTS bonus_trigger;
