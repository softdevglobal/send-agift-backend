-- Messaging: customer↔seller product inquiry and order chat (text-only MVP).
-- user_id / created_by_user_id are polymorphic (customer or seller UUID) — no single users table.
CREATE SCHEMA IF NOT EXISTS messaging;
-- Main conversation table: represents a chat thread either a product inquiry or an order
CREATE TABLE IF NOT EXISTS messaging.conversations (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Type of conversation: product inquiry or order
    type                 text NOT NULL
                         CHECK (type IN ('product_inquiry', 'order')),
    -- Status of conversation: open or closed
    status               text NOT NULL DEFAULT 'open'
                         CHECK (status IN ('open', 'closed')),
    -- Product ID: if the conversation is a product inquiry, the product ID
    product_id           uuid REFERENCES seller.products (id) ON DELETE SET NULL,
    -- Shop ID: if the conversation is a product inquiry, the shop ID
    shop_id              uuid REFERENCES seller.shops (id) ON DELETE SET NULL,
    -- Order ID: if the conversation is an order, the order ID
    order_id             uuid REFERENCES marketplace.orders (id) ON DELETE SET NULL,
    -- Order item ID: if the conversation is an order, the order item ID
    order_item_id        uuid REFERENCES marketplace.order_items (id) ON DELETE SET NULL,
    -- Created by user ID: the user who created the conversation
    created_by_user_id   uuid NOT NULL,
    -- Last message at: the timestamp of the last message in the conversation
    last_message_at      timestamptz,
    -- Created at: the timestamp of the creation of the conversation
    created_at           timestamptz NOT NULL DEFAULT now(),
    -- Updated at: the timestamp of the last update of the conversation
    updated_at           timestamptz NOT NULL DEFAULT now(),
    -- Rule: if type = 'product_inquiry', then product_id AND shop_id must be set, and order_id/order_item_id must be null
    -- Constraints for product inquiry conversations
    CONSTRAINT conversations_product_inquiry_ctx CHECK (
        type <> 'product_inquiry'
        OR (product_id IS NOT NULL AND shop_id IS NOT NULL AND order_id IS NULL AND order_item_id IS NULL)
    ),
     -- Rule: if type = 'order', then order_id, order_item_id, shop_id, AND product_id must all be set
    -- Constraints for order conversations
    CONSTRAINT conversations_order_ctx CHECK (
        type <> 'order'
        OR (order_id IS NOT NULL AND order_item_id IS NOT NULL AND shop_id IS NOT NULL AND product_id IS NOT NULL)
    )
);

-- Partial unique index: a given customer can have only ONE open product-inquiry conversation per product
-- (they can start a new one only after the old one is closed, since WHERE status = 'open' scopes the uniqueness)
-- One open product inquiry per customer+product.
-- Index for product inquiry conversations
CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_product_inquiry_open
    ON messaging.conversations (product_id, created_by_user_id)
    WHERE type = 'product_inquiry' AND status = 'open';

-- Partial unique index: each order line item can have at most one order-type conversation thread
-- (prevents duplicate chat threads for the same order item)
-- One order thread per line item (customer↔that seller).
CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_order_item
    ON messaging.conversations (order_item_id)
    WHERE type = 'order' AND order_item_id IS NOT NULL;

-- Speeds up seller inbox queries: "all conversations for my shop, most recently active first"
-- NULLS LAST pushes conversations with no messages yet to the bottom of the list
CREATE INDEX IF NOT EXISTS idx_conversations_shop_id
    ON messaging.conversations (shop_id, last_message_at DESC NULLS LAST);
-- Speeds up lookups like "show all conversation threads belonging to this order"
-- (an order can have multiple items, so multiple order-item conversations can share one order_id)
CREATE INDEX IF NOT EXISTS idx_conversations_order_id
    ON messaging.conversations (order_id);
-- Join table: tracks who is part of each conversation.
-- Typically one customer + one seller for product/order chat (admin added later via 000024 for support).
--
-- How we identify customer vs seller:
--   user_id = their UUID from customer.customers / seller.sellers (polymorphic — no FK)
--   role    = 'customer' | 'seller'  ← this is what tells you which side they are on
--
-- When a message is sent, messages.sender_user_id stores that same user_id;
-- join back here on (conversation_id, user_id) to get role for the UI.
CREATE TABLE IF NOT EXISTS messaging.conversation_participants (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Which chat room this person belongs to
    conversation_id   uuid NOT NULL REFERENCES messaging.conversations (id) ON DELETE CASCADE,
    -- Polymorphic person id (customer UUID or seller UUID — later also admin)
    user_id           uuid NOT NULL,
    -- Side of the chat: customer or seller (admin added in migration 000024)
    role              text NOT NULL
                      CHECK (role IN ('customer', 'seller')),
    -- Unread tracking: messages from others with created_at > last_read_at count as unread
    last_read_at      timestamptz,
    joined_at         timestamptz NOT NULL DEFAULT now(),
    -- Same person can't be added twice to the same room
    UNIQUE (conversation_id, user_id)
);

-- Speeds up "show all conversations this user is part of," ordered by when they joined
CREATE INDEX IF NOT EXISTS idx_conversation_participants_user
    ON messaging.conversation_participants (user_id, joined_at DESC);

-- Individual chat bubbles within a conversation.
-- Both customer and seller writes go here — only sender_user_id differs.
-- There is NO role column: look up sender_user_id in conversation_participants to know who spoke.
CREATE TABLE IF NOT EXISTS messaging.messages (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id   uuid NOT NULL REFERENCES messaging.conversations (id) ON DELETE CASCADE,
    -- JWT subject of whoever sent this bubble (customer id OR seller id)
    sender_user_id    uuid NOT NULL,
    -- Non-empty text only (MVP); file attachments come later via message_attachments
    body              text NOT NULL CHECK (char_length(trim(body)) > 0),
    type              text NOT NULL DEFAULT 'text'
                      CHECK (type IN ('text')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    -- Soft-delete: list queries skip rows where this is set
    deleted_at        timestamptz
);
-- Core index for chat pagination: fetch all messages in a conversation ordered chronologically
CREATE INDEX IF NOT EXISTS idx_messages_conversation_created
    ON messaging.messages (conversation_id, created_at ASC);
