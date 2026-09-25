-- Phase 8 rollback: drop the variant layer and restore the flat cart/order
-- shape. order_items/cart_items variant_id columns are dropped; cart_items
-- revert to one line per (cart, product). History on order_items loses the
-- variant pointer, matching the pre-Phase-8 model (the down file exists for
-- local dev rollback, not for the live database).

SET LOCAL row_security = off;

ALTER TABLE cart_items DROP CONSTRAINT cart_items_cart_id_variant_id_key;
ALTER TABLE cart_items ADD CONSTRAINT cart_items_cart_id_product_id_key UNIQUE (cart_id, product_id);

ALTER TABLE order_items DROP COLUMN variant_id;
ALTER TABLE cart_items DROP COLUMN variant_id;

DROP TABLE IF EXISTS product_variant_option_values;
DROP TABLE IF EXISTS product_variants;
DROP TABLE IF EXISTS product_option_values;
DROP TABLE IF EXISTS product_options;