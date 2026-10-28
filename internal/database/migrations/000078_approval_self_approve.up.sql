-- A Super Admin can approve an approval record they recorded themselves.
-- The record keeps who recorded it and who approved it, and notes when they
-- were the same person.
ALTER TABLE core.capability_approvals
    ADD COLUMN IF NOT EXISTS self_approved boolean NOT NULL DEFAULT false;

ALTER TABLE core.capability_approvals
    DROP CONSTRAINT IF EXISTS capability_approvals_two_people,
    ADD CONSTRAINT capability_approvals_two_people CHECK (
        approved_by_admin_id IS NULL
        OR approved_by_admin_id <> requested_by_admin_id
        OR self_approved);
