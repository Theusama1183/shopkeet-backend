ALTER TABLE orders DROP COLUMN IF EXISTS customer_id;

DROP TABLE IF EXISTS customer_addresses;
DROP TABLE IF EXISTS customers;