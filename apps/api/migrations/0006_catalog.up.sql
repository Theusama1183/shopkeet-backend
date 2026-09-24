-- Phase 3: Catalog — categories, products, product_categories, product_images.
-- Mirrors docs/schema.sql. RLS policy ships in the same migration as each
-- table (docs/02-tech-stack.md RLS rule); every tenant-scoped table is FORCE
-- RLS and owned by the app role (0003/0004 model).

-- ============================================================ categories

CREATE TABLE categories (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  name       TEXT NOT NULL,
  slug       TEXT NOT NULL,
  UNIQUE (tenant_id, slug)
);
ALTER TABLE categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE categories FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON categories
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE categories OWNER TO shopkeet_app;

-- ============================================================ products

CREATE TABLE products (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  name            TEXT NOT NULL,
  slug            TEXT NOT NULL,
  description     TEXT,
  price_cents     INTEGER NOT NULL,
  currency        TEXT NOT NULL DEFAULT 'usd', -- tenant-configurable; illustrative default
  inventory_count INTEGER NOT NULL DEFAULT 0,
  status          TEXT NOT NULL DEFAULT 'draft', -- draft, active, archived
  meta_title      TEXT,
  meta_description TEXT,
  search_vector   TSVECTOR GENERATED ALWAYS AS (
    to_tsvector('english', coalesce(name, '') || ' ' || coalesce(description, ''))
  ) STORED,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, slug)
);
CREATE INDEX products_search_idx ON products USING GIN (search_vector);
ALTER TABLE products ENABLE ROW LEVEL SECURITY;
ALTER TABLE products FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON products
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE products OWNER TO shopkeet_app;

-- ============================================================ product_categories

CREATE TABLE product_categories (
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  product_id  UUID NOT NULL REFERENCES products(id),
  category_id UUID NOT NULL REFERENCES categories(id),
  PRIMARY KEY (product_id, category_id)
);
ALTER TABLE product_categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_categories FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_categories
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE product_categories OWNER TO shopkeet_app;

-- ============================================================ product_images

CREATE TABLE product_images (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     UUID NOT NULL REFERENCES tenants(id),
  product_id    UUID NOT NULL REFERENCES products(id),
  media_asset_id UUID NOT NULL REFERENCES media_assets(id),
  sort_order    INTEGER NOT NULL DEFAULT 0,
  UNIQUE (product_id, media_asset_id)
);
ALTER TABLE product_images ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_images FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_images
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE product_images OWNER TO shopkeet_app;