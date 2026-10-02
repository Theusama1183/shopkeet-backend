ALTER TABLE order_items DROP COLUMN IF EXISTS bundle_id;
ALTER TABLE cart_items DROP COLUMN IF EXISTS bundle_id;
DROP TABLE IF EXISTS quantity_breaks;
DROP TABLE IF EXISTS bundle_items;
DROP TABLE IF EXISTS bundles;