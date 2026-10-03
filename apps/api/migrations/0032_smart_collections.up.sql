-- Phase 31: Smart (Rule-Based) Collections. A category flips to a "smart
-- collection" (categories.is_smart) with a set of AND-combined rules in
-- smart_collection_rules; products save themselves into/out of membership so
-- the storefront keeps reading product_categories (no query-time recompute).
CREATE TABLE smart_collection_rules (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  category_id UUID NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
  field       TEXT NOT NULL,                -- 'price', 'inventory_count', 'status', 'name', 'description'
  operator    TEXT NOT NULL,                -- 'lt', 'gt', 'eq', 'contains'
  value       TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, category_id, field, operator, value),
  CHECK (field IN ('price', 'inventory_count', 'status', 'name', 'description')),
  CHECK (operator IN ('lt', 'gt', 'eq', 'contains'))
);
-- RLS contract (NULLIF-wrapped, migration 0029): a stale '' custom GUC reads
-- as NULL (fail-closed zero rows) instead of erroring.
ALTER TABLE smart_collection_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE smart_collection_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON smart_collection_rules
  USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
ALTER TABLE smart_collection_rules OWNER TO shopkeet_app;

ALTER TABLE categories ADD COLUMN is_smart BOOLEAN NOT NULL DEFAULT false;