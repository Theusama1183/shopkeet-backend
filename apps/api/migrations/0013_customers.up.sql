-- Phase 11: Customer Accounts.
-- Storefront shoppers can register, log in and save addresses; orders gain an
-- optional customer_id so a registered customer can see their history. Guest
-- checkout stays fully supported (customer_id NULL). Every tenant-scoped table
-- keeps RLS + FORCE + OWNER in the same migration. Pure DDL (no FORCE-RLS DML),
-- so this runs as shopkeet_app like 0011/0012.

-- ============================================================ customers

CREATE TABLE customers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  email TEXT,              -- unique per tenant; NULL = no login set up yet
  phone TEXT,
  password_hash TEXT,      -- NULL = record not yet set up for login
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);
ALTER TABLE customers ENABLE ROW LEVEL SECURITY;
ALTER TABLE customers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customers
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE customers OWNER TO shopkeet_app;

-- ============================================================ customer_addresses

CREATE TABLE customer_addresses (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  customer_id UUID NOT NULL REFERENCES customers(id),
  label TEXT,                    -- e.g. "Home", "Office"
  address_line1 TEXT NOT NULL,
  address_line2 TEXT,
  city TEXT NOT NULL,
  state TEXT,
  postal_code TEXT,
  country TEXT NOT NULL,
  is_default BOOLEAN NOT NULL DEFAULT false
);
ALTER TABLE customer_addresses ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_addresses FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_addresses
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE customer_addresses OWNER TO shopkeet_app;
CREATE INDEX customer_addresses_customer_idx ON customer_addresses (customer_id);

-- ============================================================ existing tables

ALTER TABLE orders ADD COLUMN customer_id UUID REFERENCES customers(id); -- nullable: guest checkout still allowed