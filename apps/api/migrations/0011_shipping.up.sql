-- Phase 9: Shipping.
-- orders gains structured destination fields (line1/line2/city/state/postal/
-- country) + a shipping_method snapshot (rate name at order time) and the
-- applied shipping_cost_cents. The old flat shipping_address column is kept in
-- place as a historical record -- the API stops writing to it going forward
-- (it is made nullable so future orders may leave it NULL).
-- Every tenant-scoped table keeps RLS + FORCE + OWNER in the same migration.

-- ============================================================ orders columns

ALTER TABLE orders
  ADD COLUMN shipping_address_line1 TEXT,
  ADD COLUMN shipping_address_line2 TEXT,
  ADD COLUMN shipping_city TEXT,
  ADD COLUMN shipping_state TEXT,
  ADD COLUMN shipping_postal_code TEXT,
  ADD COLUMN shipping_country TEXT,
  ADD COLUMN shipping_method TEXT, -- snapshot of the rate name chosen at order time
  ADD COLUMN shipping_cost_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ALTER COLUMN shipping_address DROP NOT NULL;

-- ============================================================ shipping_zones

CREATE TABLE shipping_zones (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  name       TEXT NOT NULL,                       -- e.g. "Punjab", "Rest of country"
  countries  TEXT[] NOT NULL DEFAULT '{}',        -- ISO country codes covered
  regions    TEXT[] NOT NULL DEFAULT '{}',        -- optional finer state/province match
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE shipping_zones ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipping_zones FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON shipping_zones
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE shipping_zones OWNER TO shopkeet_app;

-- ============================================================ shipping_rates

CREATE TABLE shipping_rates (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  zone_id         UUID NOT NULL REFERENCES shipping_zones(id),
  name            TEXT NOT NULL,                  -- e.g. "Standard", "Express"
  rate_cents      INTEGER NOT NULL,
  free_over_cents INTEGER,                        -- NULL = never free; else free when subtotal >= this
  sort_order      INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE shipping_rates ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipping_rates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON shipping_rates
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE shipping_rates OWNER TO shopkeet_app;
CREATE INDEX shipping_rates_zone_idx ON shipping_rates (zone_id);