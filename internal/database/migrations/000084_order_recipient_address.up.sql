-- The exact address an order ships to.
--
-- Orders used to ship to whatever the recipient's default address was when
-- each query ran, so a recipient with several addresses could be quoted for
-- one and shipped to another. Checkout now sends the address that was picked;
-- it is checked to belong to the recipient and kept here. Older orders keep
-- NULL and still resolve to the recipient's default, as before.
ALTER TABLE marketplace.orders
    ADD COLUMN IF NOT EXISTS recipient_address_id uuid
        REFERENCES customer.recipient_addresses(id) ON DELETE SET NULL;
