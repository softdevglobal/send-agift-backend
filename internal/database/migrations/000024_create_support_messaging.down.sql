DROP INDEX IF EXISTS support.idx_support_cases_status;
DROP INDEX IF EXISTS support.idx_support_cases_opened_by;
DROP INDEX IF EXISTS support.idx_support_cases_counterpart_open;
DROP TABLE IF EXISTS support.cases;
DROP SCHEMA IF EXISTS support;

ALTER TABLE messaging.conversation_participants
    DROP CONSTRAINT IF EXISTS conversation_participants_role_check;
ALTER TABLE messaging.conversation_participants
    ADD CONSTRAINT conversation_participants_role_check
    CHECK (role IN ('customer', 'seller'));

ALTER TABLE messaging.conversations
    DROP CONSTRAINT IF EXISTS conversations_support_ctx;

ALTER TABLE messaging.conversations
    DROP CONSTRAINT IF EXISTS conversations_type_check;
ALTER TABLE messaging.conversations
    ADD CONSTRAINT conversations_type_check
    CHECK (type IN ('product_inquiry', 'order'));
