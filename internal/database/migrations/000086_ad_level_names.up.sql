-- Names for an advertising package's priority levels ("Spotlight",
-- "Featured", ...), set by the admin. Empty means the default names
-- (High / Medium / Low, or Level 1, 2, ...). A purchase copies them, like
-- the rest of the terms, and a published version can't change them.
ALTER TABLE ads.packages
    ADD COLUMN level_names text[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT packages_level_names CHECK (
        cardinality(level_names) = 0 OR cardinality(level_names) = product_slots
    );
ALTER TABLE ads.purchases
    ADD COLUMN level_names text[] NOT NULL DEFAULT '{}';

CREATE OR REPLACE FUNCTION ads.freeze_package() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status <> 'draft' AND (
        NEW.package_key, NEW.version, NEW.country_id, NEW.name, NEW.description, NEW.currency,
        NEW.price_minor, NEW.product_slots, NEW.level_entries, NEW.level_names, NEW.entries_pool,
        NEW.validity_days
    ) IS DISTINCT FROM (
        OLD.package_key, OLD.version, OLD.country_id, OLD.name, OLD.description, OLD.currency,
        OLD.price_minor, OLD.product_slots, OLD.level_entries, OLD.level_names, OLD.entries_pool,
        OLD.validity_days
    ) THEN
        RAISE EXCEPTION 'ads.packages: a published package version is immutable; create a new version';
    END IF;
    RETURN NEW;
END;
$$;
