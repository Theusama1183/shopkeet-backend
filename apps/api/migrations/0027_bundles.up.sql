-- Phase 25: Product Bundles & Quantity Breaks (docs/09-growth-features-build-spec.md).
-- New tenant-scoped tables keep the established RLS contract: tenant_id +
-- ENABLE/FORCE ROW LEVEL SECURITY + same-migration policy + OWNER. Exactly one
-- of bundle_price_cents (a flat price per bundle) or discount_percent (a % off
-- the summed components) is set, enforced by CHECK. Cart and order lines gain
-- a nullable bundle_id so a fixed bundle expands into one cart_items row per
-- component variant while the cart/checkout charge the whole bundle, and
-- order_items snapshot each component's real unit price for inventory.

-- ============================================================ bundles

CREATE TABLE bundles (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id),
  name               TEXT NOT NULL,
  type               TEXT NOT NULL DEFAULT 'fixed', -- fixed, mix_and_match
  bundle_price_cents INTEGER,                       -- flat price per bundle (fixed); mutually exclusive with discount_percent
  discount_percent   INTEGER,                       -- % off the summed components; mutually exclusive with bundle_price_cents
  status             TEXT NOT NULL DEFAULT 'draft', -- draft, active, archived
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (type IN ('fixed', 'mix_and_match')),
  CHECK (status IN ('draft', 'active', 'archived')),
  CHECK ((bundle_price_cents IS NULL) <> (discount_percent IS NULL)),
  CHECK (bundle_price_cents IS NULL OR bundle_price_cents > 0),
  CHECK (discount_percent IS NULL OR discount_percent BETWEEN 1 AND 100),
  UNIQUE (tenant_id, name)
);
ALTER TABLE bundles ENABLE ROW LEVEL SECURITY;
ALTER TABLE bundles FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bundles
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE bundles OWNER TO shopkeet_app;

CREATE INDEX bundles_tenant_status_idx ON bundles (tenant_id, status, created_at);

-- ============================================================ bundle_items

-- For 'fixed' bundles quantity is how many units of the product each bundle
-- contains; for 'mix_and_match' each row marks one product as part of the
-- eligible pool (quantity stays 1).
CREATE TABLE bundle_items (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  bundle_id  UUID NOT NULL REFERENCES bundles(id) ON DELETE CASCADE,
  product_id UUID NOT NULL REFERENCES products(id),
  quantity   INTEGER NOT NULL DEFAULT 1,
  CHECK (quantity > 0),
  UNIQUE (bundle_id, product_id)
);
ALTER TABLE bundle_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE bundle_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bundle_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE bundle_items OWNER TO shopkeet_app;

CREATE INDEX bundle_items_bundle_idx ON bundle_items (bundle_id);

-- ============================================================ quantity_breaks

CREATE TABLE quantity_breaks (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        UUID NOT NULL REFERENCES tenants(id),
  product_id       UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  min_quantity     INTEGER NOT NULL,
  discount_percent INTEGER NOT NULL,
  CHECK (min_quantity > 0),
  CHECK (discount_percent BETWEEN 1 AND 100),
  UNIQUE (product_id, min_quantity)
);
ALTER TABLE quantity_breaks ENABLE ROW LEVEL SECURITY;
ALTER TABLE quantity_breaks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON quantity_breaks
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE quantity_breaks OWNER TO shopkeet_app;

-- ============================================================ cart/order links

ALTER TABLE cart_items ADD COLUMN bundle_id UUID REFERENCES bundles(id);
ALTER TABLE order_items ADD COLUMN bundle_id UUID REFERENCES bundles(id);