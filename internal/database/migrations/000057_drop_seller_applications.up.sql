-- The multi-step seller application (000056) was withdrawn. Databases that
-- already applied it drop the table here; fresh ones never create it, so
-- every database ends with the same schema.
DROP TABLE IF EXISTS seller.seller_applications;
