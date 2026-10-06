ALTER TABLE seller.shops
    DROP CONSTRAINT IF EXISTS shops_working_days_check;

ALTER TABLE seller.shops
    DROP COLUMN IF EXISTS pickup_enabled,
    DROP COLUMN IF EXISTS working_days,
    DROP COLUMN IF EXISTS gift_options,
    DROP COLUMN IF EXISTS categories,
    DROP COLUMN IF EXISTS returns_policy,
    DROP COLUMN IF EXISTS support_email,
    DROP COLUMN IF EXISTS website;

-- Registered addresses are never linked to a shop, so they can be removed
-- before the original constraint comes back.
DELETE FROM seller.seller_addresses WHERE address_type = 'registered';

ALTER TABLE seller.seller_addresses
    DROP CONSTRAINT IF EXISTS seller_addresses_address_type_check;
ALTER TABLE seller.seller_addresses
    ADD CONSTRAINT seller_addresses_address_type_check
    CHECK (address_type IN ('pickup', 'return', 'both'));

DROP TABLE IF EXISTS seller.seller_tax_registrations;
DROP TABLE IF EXISTS seller.seller_identifiers;

ALTER TABLE seller.sellers
    DROP CONSTRAINT IF EXISTS sellers_contact_role_check,
    DROP CONSTRAINT IF EXISTS sellers_tax_status_check,
    DROP CONSTRAINT IF EXISTS sellers_registration_status_check;

ALTER TABLE seller.sellers
    DROP COLUMN IF EXISTS marketing_opt_in,
    DROP COLUMN IF EXISTS terms_accepted_at,
    DROP COLUMN IF EXISTS authority_confirmed_at,
    DROP COLUMN IF EXISTS contact_job_title,
    DROP COLUMN IF EXISTS contact_role,
    DROP COLUMN IF EXISTS contact_name,
    DROP COLUMN IF EXISTS tax_status,
    DROP COLUMN IF EXISTS registration_note,
    DROP COLUMN IF EXISTS registration_status,
    DROP COLUMN IF EXISTS local_name;
