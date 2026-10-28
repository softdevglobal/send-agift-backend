ALTER TABLE core.capability_approvals
    DROP CONSTRAINT IF EXISTS capability_approvals_two_people,
    ADD CONSTRAINT capability_approvals_two_people CHECK (
        approved_by_admin_id IS NULL OR approved_by_admin_id <> requested_by_admin_id),
    DROP COLUMN IF EXISTS self_approved;
