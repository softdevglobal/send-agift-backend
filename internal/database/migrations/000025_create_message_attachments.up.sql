-- Chat message file attachments (damage photos, invoices, etc.).
-- Files live in media.media_assets; this table only links them to a message.
CREATE TABLE IF NOT EXISTS messaging.message_attachments (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id  uuid NOT NULL REFERENCES messaging.messages (id) ON DELETE CASCADE,
    media_id    uuid NOT NULL REFERENCES media.media_assets (id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (message_id, media_id)
);

CREATE INDEX IF NOT EXISTS idx_message_attachments_message_id
    ON messaging.message_attachments (message_id);

-- Allow attachment-only messages (body may be empty/whitespace when files are attached).
-- Service still requires body OR at least one attachment.
ALTER TABLE messaging.messages
    DROP CONSTRAINT IF EXISTS messages_body_check;
