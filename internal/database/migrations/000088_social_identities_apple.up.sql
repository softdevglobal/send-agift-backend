-- Sign in with Apple links an Apple account to a customer the same way
-- Google and Facebook do, but the provider check from 000055 only allowed
-- those two, so every Apple sign-up failed when the link was saved.
ALTER TABLE customer.social_identities
    DROP CONSTRAINT IF EXISTS social_identities_provider_check;
ALTER TABLE customer.social_identities
    ADD CONSTRAINT social_identities_provider_check
    CHECK (provider IN ('google', 'facebook', 'apple'));
