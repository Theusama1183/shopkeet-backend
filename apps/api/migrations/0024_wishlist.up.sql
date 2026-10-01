-- Phase 22: Wishlist (Wishlist Plus replacement).
-- wishlist_items keeps the established RLS contract: tenant_id +
-- ENABLE/FORCE ROW LEVEL SECURITY + same-migration policy + OWNER. The UNIQUE
-- constraint on (customer_id, product_id) makes a duplicate add a 23505, which
-- the API answers 409 so a storefront double-tap never double-inserts.

CREATE TABLE wishlist_items (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  customer_id UUID NOT NULL REFERENCES customers(id),
  product_id  UUID NOT NULL REFERENCES products(id),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (customer_id, product_id)
);
ALTER TABLE wishlist_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE wishlist_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wishlist_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE wishlist_items OWNER TO shopkeet_app;

CREATE INDEX wishlist_customer_idx ON wishlist_items (customer_id, created_at DESC);