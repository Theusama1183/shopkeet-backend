-- Admin command palette — record search. orders and customers gain the same
-- generated tsvector + GIN index that products got in 0006, so GET /search
-- (internal/search) can match all three record groups with the same fast path.
-- GENERATED ALWAYS AS … STORED computes each row's value on write, and ADD
-- COLUMN backfills existing rows in the same ALTER (the expression is
-- immutable: explicit 'english' config + coalesce). Pure DDL + new indexes on
-- existing tables — orders/customers already carry RLS + FORCE + OWNER from
-- 0008/0013 — so this runs as shopkeet_app like 0011/0012.

ALTER TABLE orders ADD COLUMN search_vector TSVECTOR GENERATED ALWAYS AS (
  to_tsvector('english',
    coalesce(customer_name, '') || ' ' ||
    coalesce(customer_phone, '') || ' ' ||
    coalesce(customer_email, '') || ' ' ||
    coalesce(shipping_address, '')
  )
) STORED;
CREATE INDEX orders_search_idx ON orders USING GIN (search_vector);

ALTER TABLE customers ADD COLUMN search_vector TSVECTOR GENERATED ALWAYS AS (
  to_tsvector('english', coalesce(email, '') || ' ' || coalesce(phone, ''))
) STORED;
CREATE INDEX customers_search_idx ON customers USING GIN (search_vector);