-- Support chat: admin ↔ customer or admin ↔ seller.
-- Reuses messaging.conversations / conversation_participants / messages for the actual chat.
-- support.cases holds ticket metadata (subject, status, priority, who the case is about).
CREATE SCHEMA IF NOT EXISTS support;

-- Expand allowed conversation types to include support (was product_inquiry | order only).
ALTER TABLE messaging.conversations
    DROP CONSTRAINT IF EXISTS conversations_type_check;
ALTER TABLE messaging.conversations
    ADD CONSTRAINT conversations_type_check
    CHECK (type IN ('product_inquiry', 'order', 'support'));

-- Support threads must NOT carry product/order anchors (those belong on product_inquiry / order).
ALTER TABLE messaging.conversations
    DROP CONSTRAINT IF EXISTS conversations_support_ctx;
ALTER TABLE messaging.conversations
    ADD CONSTRAINT conversations_support_ctx CHECK (
        type <> 'support'
        OR (
            product_id IS NULL
            AND shop_id IS NULL
            AND order_id IS NULL
            AND order_item_id IS NULL
        )
    );

-- Allow admins in the participant role set (was customer | seller only in 000023).
-- participant.user_id for role=admin points at admin.admin_users.id.
ALTER TABLE messaging.conversation_participants
    DROP CONSTRAINT IF EXISTS conversation_participants_role_check;
ALTER TABLE messaging.conversation_participants
    ADD CONSTRAINT conversation_participants_role_check
    CHECK (role IN ('customer', 'seller', 'admin'));

-- One support ticket per conversation (1:1). Chat bubbles stay in messaging.messages.
CREATE TABLE IF NOT EXISTS support.cases (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Links this ticket to its messaging.conversations room
    conversation_id       uuid NOT NULL UNIQUE
                          REFERENCES messaging.conversations (id) ON DELETE CASCADE,
    -- Who opened the ticket (admin reaching out, or customer/seller asking for help)
    opened_by_user_id     uuid NOT NULL,
    opened_by_role        text NOT NULL
                          CHECK (opened_by_role IN ('admin', 'customer', 'seller')),
    -- Who the ticket is ABOUT (always a customer or seller — never an admin)
    counterpart_user_id   uuid NOT NULL,
    counterpart_role      text NOT NULL
                          CHECK (counterpart_role IN ('customer', 'seller')),
    subject               text, -- optional free-text title
    status                text NOT NULL DEFAULT 'open'
                          CHECK (status IN ('open', 'in_progress', 'closed')),
    priority              text NOT NULL DEFAULT 'normal'
                          CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- One active support thread per customer or seller (can't open a second while one is open/in_progress).
CREATE UNIQUE INDEX IF NOT EXISTS idx_support_cases_counterpart_open
    ON support.cases (counterpart_role, counterpart_user_id)
    WHERE status IN ('open', 'in_progress');

-- "Cases I opened" lookups
CREATE INDEX IF NOT EXISTS idx_support_cases_opened_by
    ON support.cases (opened_by_user_id, created_at DESC);

-- Ops / admin queue filtered by status
CREATE INDEX IF NOT EXISTS idx_support_cases_status
    ON support.cases (status, updated_at DESC);
