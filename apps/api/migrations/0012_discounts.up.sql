-- Phase 10: Discounts.
-- Admin-managed discount codes (percentage or fixed amount) with a minimum
-- subtotal, a validity window, and an optional usage cap. carts records the
-- code a guest applied; orders snapshots the applied code + computed
-- discount_cents at checkout. Every tenant-scoped table keeps RLS + FORCE +
-- OWNER in the same migration.

-- ============================================================ discounts

CREATE TABLE discounts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  code TEXT NOT NULL,
  type TEXT NOT NULL,                  -- 'percentage', 'fixed_amount'
  value_percent INTEGER,               -- for 'percentage', 1-100
  value_cents INTEGER,                 -- for 'fixed_amount'
  min_subtotal_cents INTEGER,          -- NULL = no minimum
  starts_at TIMESTAMPTZ,               -- NULL = valid from the beginning
  ends_at TIMESTAMPTZ,                 -- NULL = no expiry
  usage_limit INTEGER,                 -- NULL = unlimited
  times_used INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active', -- active, disabled
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (type IN ('percentage', 'fixed_amount')),
  CHECK (type <> 'percentage' OR value_percent BETWEEN 1 AND 100),
  CHECK (type <> 'fixed_amount' OR value_cents > 0),
  CHECK (status IN ('active', 'disabled')),
  UNIQUE (tenant_id, code)
);
ALTER TABLE discounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE discounts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON discounts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE discounts OWNER TO shopkeet_app;

-- ============================================================ existing tables

ALTER TABLE carts  ADD COLUMN discount_code TEXT;
ALTER TABLE orders ADD COLUMN discount_code TEXT,
                   ADD COLUMN discount_cents INTEGER NOT NULL DEFAULT 0;