-- Removes competition points. Only safe before any have been granted: the
-- lots and ledger are the record of points customers hold.
DROP TABLE IF EXISTS core.emergency_stops;
DROP TABLE IF EXISTS core.capability_approvals;
DROP TABLE IF EXISTS competition.cp_entry_lots;
DROP TABLE IF EXISTS competition.cp_entries;
ALTER TABLE competition.competitions
    DROP CONSTRAINT IF EXISTS competitions_cp_sources,
    DROP COLUMN IF EXISTS cp_allowed_sources,
    DROP COLUMN IF EXISTS points_unit;
DROP TABLE IF EXISTS points.cp_review_cases;
DROP TABLE IF EXISTS points.cp_postings;
DROP TABLE IF EXISTS points.cp_transactions;
ALTER TABLE IF EXISTS membership.bonus_slots DROP CONSTRAINT IF EXISTS bonus_slots_lot_fk;
ALTER TABLE IF EXISTS points.gift_qualifications DROP CONSTRAINT IF EXISTS gift_qualifications_base_lot_fk;
DROP TABLE IF EXISTS points.cp_lots;
ALTER TABLE IF EXISTS membership.bonus_slots DROP CONSTRAINT IF EXISTS bonus_slots_gift_fk;
DROP TABLE IF EXISTS points.gift_qualifications;
DROP TABLE IF EXISTS points.reward_policies;
DROP FUNCTION IF EXISTS points.freeze_reward_policy();
DROP FUNCTION IF EXISTS points.check_cp_balanced();
DROP FUNCTION IF EXISTS points.forbid_cp_mutation();
DROP SCHEMA IF EXISTS membership CASCADE;
