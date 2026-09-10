DROP INDEX IF EXISTS messaging.idx_message_attachments_message_id;
DROP TABLE IF EXISTS messaging.message_attachments;

-- Restore non-empty body rule from 000023 (best-effort; empty rows may already exist).
ALTER TABLE messaging.messages
    DROP CONSTRAINT IF EXISTS messages_body_check;
ALTER TABLE messaging.messages
    ADD CONSTRAINT messages_body_check CHECK (char_length(trim(body)) > 0);
