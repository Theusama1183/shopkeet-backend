-- Phase 4: Cart & Inventory — guest carts keyed by an opaque customer session
-- (cookie-backed; guest checkout supported, no account required for v1).
-- Mirrors docs/schema.sql carts/cart_items. RLS policy ships in the same
-- migration as the table (docs/02-tech-stack.md RLS rule); FORCE + owner match
-- the 0003/0004 model so shopkeet_app is never silently exempt.

CREATE TABLE carts (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        UUID NOT NULL REFERENCES tenants(id),
  customer_session TEXT NOT NULL, -- opaque cookie-backed id; guest checkout supported
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, customer_session)
);

CREATE TABLE cart_items (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  cart_id    UUID NOT NULL REFERENCES carts(id),
  product_id UUID NOT NULL REFERENCES products(id),
  quantity   INTEGER NOT NULL CHECK (quantity > 0),
  UNIQUE (cart_id, product_id)
);

ALTER TABLE carts ENABLE ROW LEVEL SECURITY;
ALTER TABLE carts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON carts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE cart_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE cart_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON cart_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE carts OWNER TO shopkeet_app;
ALTER TABLE cart_items OWNER TO shopkeet_app;