-- Reel likes + comments (TikTok/AliExpress-style social on seller.reels).
-- Same endpoints for guests and logged-in customers; identity is JWT or X-Guest-Token.

CREATE SCHEMA IF NOT EXISTS social;

-- Denormalized counters for fast feed reads (updated in the same TX as like/comment writes).
ALTER TABLE seller.reels
    ADD COLUMN IF NOT EXISTS like_count    bigint NOT NULL DEFAULT 0 CHECK (like_count >= 0),
    ADD COLUMN IF NOT EXISTS comment_count bigint NOT NULL DEFAULT 0 CHECK (comment_count >= 0);

-- ─── Likes: one row per reel + identity (customer XOR guest) ───────────────
CREATE TABLE IF NOT EXISTS social.reel_likes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    reel_id      uuid NOT NULL REFERENCES seller.reels (id) ON DELETE CASCADE,
    -- Logged-in customer (role=customer JWT). Null for guests.
    customer_id  uuid REFERENCES customer.customers (id) ON DELETE CASCADE,
    -- Stable UUID from the app (X-Guest-Token). Null for customers.
    guest_token  text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- Exactly one identity must be set.
    CHECK (
        (customer_id IS NOT NULL AND guest_token IS NULL)
        OR (customer_id IS NULL AND guest_token IS NOT NULL)
    )
);

-- One like per customer per reel.
CREATE UNIQUE INDEX IF NOT EXISTS uq_reel_likes_customer
    ON social.reel_likes (reel_id, customer_id)
    WHERE customer_id IS NOT NULL;

-- One like per guest token per reel.
CREATE UNIQUE INDEX IF NOT EXISTS uq_reel_likes_guest
    ON social.reel_likes (reel_id, guest_token)
    WHERE guest_token IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_reel_likes_reel_id
    ON social.reel_likes (reel_id);

-- ─── Comments: public; author can edit/delete via same JWT or guest token ─
CREATE TABLE IF NOT EXISTS social.reel_comments (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    reel_id       uuid NOT NULL REFERENCES seller.reels (id) ON DELETE CASCADE,
    -- Logged-in customer; kept even when is_anonymous for moderation/edit rights.
    customer_id   uuid REFERENCES customer.customers (id) ON DELETE SET NULL,
    -- Guest author; used to authorize PUT/DELETE without login.
    guest_token   text,
    -- true = hide real identity in API (guests are always anonymous).
    is_anonymous  boolean NOT NULL DEFAULT false,
    -- Nickname for anonymous posts; ignored in public JSON when not anonymous.
    display_name  text,
    body          text NOT NULL CHECK (char_length(trim(body)) BETWEEN 1 AND 1000),
    status        text NOT NULL DEFAULT 'visible'
                  CHECK (status IN ('visible', 'hidden', 'deleted')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- Must be either a customer (named or anon) or a guest (always anon).
    CHECK (
        (customer_id IS NOT NULL AND guest_token IS NULL)
        OR (customer_id IS NULL AND guest_token IS NOT NULL AND is_anonymous = true)
    )
);

-- Newest visible comments first for a reel.
CREATE INDEX IF NOT EXISTS idx_reel_comments_reel_created
    ON social.reel_comments (reel_id, created_at DESC)
    WHERE status = 'visible';
