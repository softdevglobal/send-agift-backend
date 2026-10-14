-- A delivered gift can be reviewed by the person who bought it and by the
-- person it was sent to, each once. Until now the order line allowed a single
-- review, so whoever reviewed first blocked the other.
--
-- The old rule was written inline on the column, so its name is found by what
-- it covers rather than assumed.
DO $$
DECLARE
    c text;
BEGIN
    FOR c IN
        SELECT con.conname
        FROM pg_constraint con
        WHERE con.conrelid = 'marketplace.product_reviews'::regclass
          AND con.contype = 'u'
          AND con.conkey = ARRAY[(
              SELECT attnum FROM pg_attribute
              WHERE attrelid = 'marketplace.product_reviews'::regclass
                AND attname = 'order_item_id')]
    LOOP
        EXECUTE format('ALTER TABLE marketplace.product_reviews DROP CONSTRAINT %I', c);
    END LOOP;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS product_reviews_item_reviewer_uq
    ON marketplace.product_reviews (order_item_id, customer_id);
