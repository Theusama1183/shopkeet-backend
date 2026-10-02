-- Phase 26: Upsell, Cross-sell & Post-Purchase Offers
-- (docs/09-growth-features-build-spec.md). Merchant-curated product
-- recommendations (manual) power the storefront "you may also like" rail and
-- the order-confirmation upsell; type='auto' rows are reserved for the Phase 32
-- scheduled job. Keeps the established RLS contract: tenant_id + ENABLE/FORCE
-- ROW LEVEL SECURITY + same-migration policy + OWNER. Recommended products are
-- soft-managed (products are archived, never hard-deleted), so both FKs point
-- at products without cascade.

CREATE TABLE product_recommendations (
  id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id              UUID NOT NULL REFERENCES tenants(id),
  product_id             UUID NOT NULL REFERENCES products(id),
  recommended_product_id UUID NOT NULL REFERENCES products(id),
  type                   TEXT NOT NULL DEFAULT 'manual', -- manual (merchant-curated), auto (Phase 32 job)
  sort_order             INTEGER NOT NULL DEFAULT 0,
  CHECK (type IN ('manual', 'auto')),
  UNIQUE (product_id, recommended_product_id, type)
);
ALTER TABLE product_recommendations ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_recommendations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_recommendations
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE product_recommendations OWNER TO shopkeet_app;

CREATE INDEX recommendations_product_idx
  ON product_recommendations (product_id, type, sort_order);