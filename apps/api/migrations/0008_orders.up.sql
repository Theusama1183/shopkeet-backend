-- Phase 5: Checkout & Orders — Cash on Delivery only for v1. Checkout validates
-- stock with SELECT ... FOR UPDATE inside the request transaction (the real
-- anti-oversell guard), snapshots unit prices into order_items, decrements
-- inventory, and clears the cart. Mirrors docs/schema.sql orders/order_items.
-- RLS policy ships in the same migration (docs/02-tech-stack.md); FORCE + owner
-- match the 0003/0004 model.

CREATE TABLE orders (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  customer_name   TEXT NOT NULL,
  customer_phone  TEXT NOT NULL,
  customer_email  TEXT,
  shipping_address TEXT NOT NULL,
  payment_method  TEXT NOT NULL DEFAULT 'cod',
  payment_status  TEXT NOT NULL DEFAULT 'pending', -- pending, paid, failed
  status          TEXT NOT NULL DEFAULT 'pending', -- pending, confirmed, shipped, delivered, cancelled
  total_cents     INTEGER NOT NULL,
  currency        TEXT NOT NULL DEFAULT 'usd', -- tenant-configurable; illustrative default
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE order_items (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  order_id        UUID NOT NULL REFERENCES orders(id),
  product_id      UUID NOT NULL REFERENCES products(id),
  quantity        INTEGER NOT NULL,
  unit_price_cents INTEGER NOT NULL -- snapshot of products.price_cents at checkout
);

ALTER TABLE orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE orders FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON orders
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE order_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON order_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE orders OWNER TO shopkeet_app;
ALTER TABLE order_items OWNER TO shopkeet_app;