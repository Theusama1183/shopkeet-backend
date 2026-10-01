-- Phase 24 — Storefront Analytics Dashboard.
--
-- No new tables: the dashboard aggregates orders/order_items/carts that already
-- exist. These GROUP BY date_trunc / join queries get slow fast once a store has
-- real volume, so the three hot access paths get explicit indexes. RLS does the
-- tenant scoping (every analytics query must already run inside a tenant SET
-- LOCAL tx).

-- Sales / conversion slice by tenant + created_at window.
CREATE INDEX orders_created_at_idx ON orders (tenant_id, created_at DESC);

-- top-products joins order_items back to orders by order_id. PostgreSQL does
-- NOT auto-index the referencing side of an FK (it indexes the referenced side
-- for the FK check), so this is genuinely needed despite the build spec's "it
-- is, via the FK" claim.
CREATE INDEX order_items_order_idx ON order_items (order_id);

-- Conversion counts carts created in the window.
CREATE INDEX carts_created_at_idx ON carts (tenant_id, created_at);