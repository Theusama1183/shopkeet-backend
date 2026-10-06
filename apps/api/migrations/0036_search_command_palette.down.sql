DROP INDEX IF EXISTS orders_search_idx;
ALTER TABLE orders DROP COLUMN IF EXISTS search_vector;

DROP INDEX IF EXISTS customers_search_idx;
ALTER TABLE customers DROP COLUMN IF EXISTS search_vector;