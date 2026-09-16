-- AliExpress-style product reviews: 5-star overall + quality/shipping/service,
-- optional photos via media.media_assets, and helpful/not-helpful votes.
-- One review per purchased order line (verified purchase).

CREATE TABLE IF NOT EXISTS marketplace.product_reviews (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id               uuid NOT NULL REFERENCES seller.products (id),
    shop_id                  uuid NOT NULL REFERENCES seller.shops (id),
    seller_id                uuid NOT NULL REFERENCES seller.sellers (id),
    customer_id              uuid NOT NULL REFERENCES customer.customers (id),
    order_id                 uuid NOT NULL REFERENCES marketplace.orders (id) ON DELETE CASCADE,
    order_item_id            uuid NOT NULL UNIQUE REFERENCES marketplace.order_items (id) ON DELETE CASCADE,

    -- Overall stars shown on the product card / summary.
    rating                   smallint NOT NULL CHECK (rating BETWEEN 1 AND 5),

    -- Breakdown ratings (AliExpress-style).
    product_quality_rating   smallint NOT NULL CHECK (product_quality_rating BETWEEN 1 AND 5),
    shipping_rating          smallint NOT NULL CHECK (shipping_rating BETWEEN 1 AND 5),
    seller_service_rating    smallint NOT NULL CHECK (seller_service_rating BETWEEN 1 AND 5),

    title                    text,
    body                     text,
    is_anonymous             boolean NOT NULL DEFAULT false,
    status                   text NOT NULL DEFAULT 'published'
                             CHECK (status IN ('pending', 'published', 'hidden', 'rejected')),
    seller_reply             text,
    seller_replied_at        timestamptz,
    helpful_count            integer NOT NULL DEFAULT 0 CHECK (helpful_count >= 0),
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_product_reviews_product
    ON marketplace.product_reviews (product_id, created_at DESC)
    WHERE status = 'published';

CREATE INDEX IF NOT EXISTS idx_product_reviews_shop
    ON marketplace.product_reviews (shop_id, created_at DESC)
    WHERE status = 'published';

CREATE INDEX IF NOT EXISTS idx_product_reviews_seller
    ON marketplace.product_reviews (seller_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_product_reviews_customer
    ON marketplace.product_reviews (customer_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_product_reviews_order
    ON marketplace.product_reviews (order_id);

-- Ordered photo/video attachments for one review (files live in media.media_assets).
CREATE TABLE IF NOT EXISTS marketplace.product_review_media (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    review_id       uuid NOT NULL REFERENCES marketplace.product_reviews (id) ON DELETE CASCADE,
    media_asset_id  uuid NOT NULL REFERENCES media.media_assets (id) ON DELETE CASCADE,
    position        integer NOT NULL DEFAULT 0 CHECK (position >= 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (review_id, position),
    UNIQUE (review_id, media_asset_id)
);

CREATE INDEX IF NOT EXISTS idx_product_review_media_review_id
    ON marketplace.product_review_media (review_id, position);

-- Helpful / not-helpful votes: one vote per customer per review.
CREATE TABLE IF NOT EXISTS marketplace.product_review_votes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    review_id    uuid NOT NULL REFERENCES marketplace.product_reviews (id) ON DELETE CASCADE,
    customer_id  uuid NOT NULL REFERENCES customer.customers (id) ON DELETE CASCADE,
    is_helpful   boolean NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (review_id, customer_id)
);

CREATE INDEX IF NOT EXISTS idx_product_review_votes_review_id
    ON marketplace.product_review_votes (review_id);
