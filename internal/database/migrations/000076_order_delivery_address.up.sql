-- The address a customer picked for an order. NULL falls back to the
-- recipient's default address, which is what older orders used.
ALTER TABLE marketplace.orders
    ADD COLUMN IF NOT EXISTS delivery_address_id uuid
        REFERENCES customer.recipient_addresses (id) ON DELETE SET NULL;
