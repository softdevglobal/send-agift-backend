-- Restores the shop and recipient addresses changed for Shippo testing.
-- Run against sendagift_db.

UPDATE seller.seller_addresses SET
    line1       = 'Warehouse',
    line2       = NULL,
    city        = 'Kandy',
    region      = 'Central Province',
    postal_code = '',
    country_id  = (SELECT id FROM core.countries WHERE iso_code = 'LK')
WHERE id = 'baa5a0de-1342-4c1a-9020-7b61fd1fb794';

UPDATE customer.recipient_addresses SET
    line1       = 'Melbourne Exhibition Centre',
    line2       = NULL,
    city        = 'South Wharf',
    region      = 'Victoria',
    postal_code = '3006',
    country_id  = (SELECT id FROM core.countries WHERE iso_code = 'AU')
WHERE id = '0cb03d7b-9f40-4c11-bbad-25a07205429d';

-- Optional: remove the US row this testing added (only if nothing else uses it).
-- DELETE FROM core.countries WHERE iso_code = 'US';
