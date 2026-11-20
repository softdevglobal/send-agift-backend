-- Apple links cannot survive the narrower check, so they go first.
DELETE FROM customer.social_identities WHERE provider = 'apple';
ALTER TABLE customer.social_identities
    DROP CONSTRAINT IF EXISTS social_identities_provider_check;
ALTER TABLE customer.social_identities
    ADD CONSTRAINT social_identities_provider_check
    CHECK (provider IN ('google', 'facebook'));
