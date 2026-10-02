-- Phase 28: Custom Fields (Metafields). Merchants attach arbitrary key/value
-- pairs (material, care instructions, spec sheets, …) to a product without a
-- schema change. Public GET /products/:id embeds them in the response.
CREATE TABLE product_metafields (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  key TEXT NOT NULL,                  -- e.g. 'material', 'care_instructions'
  value TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'text',  -- 'text', 'number', 'boolean', 'json'
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (product_id, key)
);
-- RLS contract (NULLIF-wrapped, migration 0029): a stale '' custom GUC in a
-- pooled session reads as NULL (fail-closed zero rows) instead of erroring.
ALTER TABLE product_metafields ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_metafields FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_metafields
  USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
ALTER TABLE product_metafields OWNER TO shopkeet_app;