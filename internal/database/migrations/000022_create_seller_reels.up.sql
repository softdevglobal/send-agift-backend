-- Seller reels: short product videos/photos shown to customers in a public feed.
-- The reel row is the post; the actual files live in media.media_assets.
CREATE TABLE IF NOT EXISTS seller.reels (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    seller_id           uuid NOT NULL REFERENCES seller.sellers (id) ON DELETE CASCADE,
    shop_id             uuid NOT NULL REFERENCES seller.shops (id) ON DELETE CASCADE,
    product_id          uuid REFERENCES seller.products (id) ON DELETE SET NULL,
    thumbnail_media_id  uuid REFERENCES media.media_assets (id) ON DELETE SET NULL,
    reel_type           text NOT NULL DEFAULT 'video'
                        CHECK (reel_type IN ('video', 'photo')),
    caption             text,
    hashtags            text[] NOT NULL DEFAULT '{}',
    visibility          text NOT NULL DEFAULT 'public'
                        CHECK (visibility IN ('public', 'private')),
    status              text NOT NULL DEFAULT 'draft'
                        CHECK (status IN ('draft', 'published', 'archived')),
    duration_ms         integer CHECK (duration_ms IS NULL OR duration_ms >= 0),
    view_count          bigint NOT NULL DEFAULT 0 CHECK (view_count >= 0),
    published_at        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- Ordered media items for one reel (single video, or a photo carousel).
CREATE TABLE IF NOT EXISTS seller.reel_media (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    reel_id         uuid NOT NULL REFERENCES seller.reels (id) ON DELETE CASCADE,
    media_asset_id  uuid NOT NULL REFERENCES media.media_assets (id) ON DELETE CASCADE,
    position        integer NOT NULL DEFAULT 0 CHECK (position >= 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (reel_id, position),
    UNIQUE (reel_id, media_asset_id)
);

CREATE INDEX IF NOT EXISTS idx_reels_seller_id
    ON seller.reels (seller_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_reels_shop_id
    ON seller.reels (shop_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_reels_product_id
    ON seller.reels (product_id);

-- Feed index: newest published public reels first.
CREATE INDEX IF NOT EXISTS idx_reels_feed
    ON seller.reels (published_at DESC, id DESC)
    WHERE status = 'published' AND visibility = 'public';

CREATE INDEX IF NOT EXISTS idx_reel_media_reel_id
    ON seller.reel_media (reel_id, position);
