-- Phase 16: Product Reviews (Judge.me replacement).
-- product_reviews keeps the established RLS contract: tenant_id +
-- ENABLE/FORCE ROW LEVEL SECURITY + same-migration policy + OWNER.
-- products gains the storefront-facing rating aggregates; they are recomputed
-- whenever a review is published/unpublished/deleted, never on pending create.
-- An order_id on a review marks it a verified purchase.

CREATE TABLE product_reviews (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id             UUID NOT NULL REFERENCES tenants(id),
  product_id            UUID NOT NULL REFERENCES products(id),
  customer_id           UUID REFERENCES customers(id),
  order_id              UUID REFERENCES orders(id),   -- present = verified purchase
  rating                INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
  title                 TEXT,
  body                  TEXT,
  photo_media_asset_ids UUID[] NOT NULL DEFAULT '{}',
  status                TEXT NOT NULL DEFAULT 'pending',  -- pending, published, rejected
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE product_reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_reviews FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_reviews
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE product_reviews OWNER TO shopkeet_app;

CREATE INDEX product_reviews_product_idx
  ON product_reviews (product_id, status, created_at DESC);

ALTER TABLE products
  ADD COLUMN rating_average NUMERIC(2,1) NOT NULL DEFAULT 0,
  ADD COLUMN rating_count   INTEGER     NOT NULL DEFAULT 0;