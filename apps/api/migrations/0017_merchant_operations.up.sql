-- Phase 15: Merchant Operations — Draft Orders & Returns.
-- Merchants take phone/WhatsApp orders outside the storefront as draft orders
-- (orders.source = 'draft'); customers (or merchants on their behalf) file
-- returns that restock inventory once marked received. Every tenant-scoped
-- table keeps RLS + FORCE + OWNER in the same migration. Pure DDL (no
-- FORCE-RLS DML), so this runs as shopkeet_app like 0011/0012/0013.

-- ============================================================ orders.source

-- 'storefront' = placed via checkout; 'draft' = created by a merchant in the admin.
ALTER TABLE orders ADD COLUMN source TEXT NOT NULL DEFAULT 'storefront';

-- ============================================================ returns

CREATE TABLE returns (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  order_id UUID NOT NULL REFERENCES orders(id),
  reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'requested', -- requested | approved | received | refunded | rejected
  restock BOOLEAN NOT NULL DEFAULT true,    -- true: marking received restocks the returned variants
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE returns ENABLE ROW LEVEL SECURITY;
ALTER TABLE returns FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON returns
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE returns OWNER TO shopkeet_app;
CREATE INDEX returns_tenant_status_idx ON returns (tenant_id, status);
CREATE INDEX returns_order_idx ON returns (order_id);

-- ============================================================ return_items

CREATE TABLE return_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  return_id UUID NOT NULL REFERENCES returns(id),
  order_item_id UUID NOT NULL REFERENCES order_items(id),
  quantity INT NOT NULL CHECK (quantity > 0)
);
ALTER TABLE return_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE return_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON return_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE return_items OWNER TO shopkeet_app;
CREATE INDEX return_items_return_idx ON return_items (return_id);
CREATE INDEX return_items_order_item_idx ON return_items (order_item_id);