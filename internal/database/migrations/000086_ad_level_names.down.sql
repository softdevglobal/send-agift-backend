CREATE OR REPLACE FUNCTION ads.freeze_package() RETURNS trigger
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
ALTER TABLE ads.purchases DROP COLUMN IF EXISTS level_names;
ALTER TABLE ads.packages DROP CONSTRAINT IF EXISTS packages_level_names, DROP COLUMN IF EXISTS level_names;
