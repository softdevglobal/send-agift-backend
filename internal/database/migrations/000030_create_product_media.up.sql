-- Ordered images/videos for one product. Files live in media.media_assets;
-- seller.products.image_url stays as the cover thumbnail for list cards / orders.
CREATE TABLE IF NOT EXISTS seller.product_media (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id      uuid NOT NULL REFERENCES seller.products (id) ON DELETE CASCADE,
    media_asset_id  uuid NOT NULL REFERENCES media.media_assets (id) ON DELETE CASCADE,
    position        integer NOT NULL DEFAULT 0 CHECK (position >= 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (product_id, position),
    UNIQUE (product_id, media_asset_id)
);

CREATE INDEX IF NOT EXISTS idx_product_media_product_id
    ON seller.product_media (product_id, position);
