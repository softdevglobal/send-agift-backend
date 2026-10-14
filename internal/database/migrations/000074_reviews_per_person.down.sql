DROP INDEX IF EXISTS marketplace.product_reviews_item_reviewer_uq;
-- Back to one review per order line. Fails if a line now has two; remove the
-- extra review first.
ALTER TABLE marketplace.product_reviews
    ADD CONSTRAINT product_reviews_order_item_id_key UNIQUE (order_item_id);
