-- Shopkeet — full database schema (source of truth)
-- Apply as sequential golang-migrate files in this order; this file is the
-- consolidated reference, not the migration files themselves.
-- Every tenant-scoped table carries tenant_id directly (denormalized onto
-- child tables too) so every RLS policy follows the same simple shape —
-- this trades a little redundancy for airtight, uniform isolation.

CREATE EXTENSION IF NOT EXISTS pgcrypto; -- gen_random_uuid()

-- ============================================================
-- Tenants (root table — not itself RLS-scoped; every other table
-- points back to it, and access to a tenant's own row is scoped
-- by matching id against the JWT's tenant_id at the query layer)
-- ============================================================

CREATE TABLE tenants (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  subdomain TEXT UNIQUE NOT NULL,
  custom_domain TEXT UNIQUE,
  status TEXT NOT NULL DEFAULT 'active', -- active, suspended
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ============================================================
-- Tenants & Auth (Phase 1)
-- ============================================================

CREATE TABLE merchant_users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  email TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'owner', -- owner, staff
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);
ALTER TABLE merchant_users ENABLE ROW LEVEL SECURITY;
-- RLS is meaningful only when the app's DB session is NOT a superuser (the
-- shell can bypass row security entirely, FORCE or not). Phase 1 adds a
-- dedicated non-superuser role `shopkeet_app` that owns these tables and
-- which the API connects as; FORCE additionally binds the table owner.
ALTER TABLE merchant_users FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON merchant_users
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ============================================================
-- Media library — Cloudflare R2 (Phase 2)
-- Not open source (flagged deliberately — see docs/02-tech-stack.md).
-- The Go API only ever stores metadata; bytes go client -> R2 directly
-- via a presigned URL, never proxied through the API.
-- ============================================================

CREATE TABLE media_assets (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  r2_key TEXT NOT NULL,          -- object key inside the R2 bucket, e.g. "{tenant_id}/{uuid}.jpg"
  url TEXT NOT NULL,             -- public URL (R2 public bucket URL or custom domain via Cloudflare)
  content_type TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  alt_text TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (r2_key)
);
ALTER TABLE media_assets ENABLE ROW LEVEL SECURITY;
ALTER TABLE media_assets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON media_assets
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ============================================================
-- Catalog (Phase 3)
-- ============================================================

CREATE TABLE categories (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  UNIQUE (tenant_id, slug)
);
ALTER TABLE categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE categories FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON categories
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE products (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  description TEXT,
  price_cents INTEGER NOT NULL,
  currency TEXT NOT NULL DEFAULT 'usd', -- tenant-configurable; illustrative default
  inventory_count INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'draft', -- draft, active, archived
  meta_title TEXT,
  meta_description TEXT,
  search_vector TSVECTOR GENERATED ALWAYS AS (
    to_tsvector('english', coalesce(name, '') || ' ' || coalesce(description, ''))
  ) STORED,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, slug)
);
CREATE INDEX products_search_idx ON products USING GIN (search_vector);
ALTER TABLE products ENABLE ROW LEVEL SECURITY;
ALTER TABLE products FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON products
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE product_categories (
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  category_id UUID NOT NULL REFERENCES categories(id),
  PRIMARY KEY (product_id, category_id)
);
ALTER TABLE product_categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_categories FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_categories
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE product_images (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  media_asset_id UUID NOT NULL REFERENCES media_assets(id),
  sort_order INTEGER NOT NULL DEFAULT 0,
  UNIQUE (product_id, media_asset_id)
);
ALTER TABLE product_images ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_images FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_images
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ============================================================
-- Cart & Inventory (Phase 4)
-- ============================================================

CREATE TABLE carts (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        UUID NOT NULL REFERENCES tenants(id),
  customer_session TEXT NOT NULL, -- opaque cookie-backed id; guest checkout supported
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, customer_session) -- one cart per guest session
);
ALTER TABLE carts ENABLE ROW LEVEL SECURITY;
ALTER TABLE carts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON carts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE cart_items (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  cart_id    UUID NOT NULL REFERENCES carts(id),
  product_id UUID NOT NULL REFERENCES products(id),
  quantity   INTEGER NOT NULL CHECK (quantity > 0),
  UNIQUE (cart_id, product_id) -- one line per product in a cart; adds merge quantity
);
ALTER TABLE cart_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE cart_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON cart_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ============================================================
-- Orders & Payments — Cash on Delivery only (Phase 5)
-- ============================================================

CREATE TABLE orders (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  customer_name TEXT NOT NULL,
  customer_phone TEXT NOT NULL,
  customer_email TEXT,
  shipping_address TEXT NOT NULL,
  payment_method TEXT NOT NULL DEFAULT 'cod',
  payment_status TEXT NOT NULL DEFAULT 'pending', -- pending, paid, failed
  status TEXT NOT NULL DEFAULT 'pending', -- pending, confirmed, shipped, delivered, cancelled
  total_cents INTEGER NOT NULL,
  currency TEXT NOT NULL DEFAULT 'usd', -- tenant-configurable; illustrative default
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE orders FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON orders
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE order_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  order_id UUID NOT NULL REFERENCES orders(id),
  product_id UUID NOT NULL REFERENCES products(id),
  quantity INTEGER NOT NULL,
  unit_price_cents INTEGER NOT NULL
);
ALTER TABLE order_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON order_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ============================================================
-- Content & Page Builder (Phase 6)
-- Three distinct concepts — see docs/04-agent-build-spec.md Phase 6:
--   posts     — one-off content (Page, Blog Post)
--   templates — rendering rules applied across many instances
--               (Product, Product Archive, Cart, 404, Order Confirmation, ...)
--   sections  — global chrome not tied to one route (Header, Footer,
--               announcement bars, popups)
-- All three store their content as a Puck-compatible JSON tree in `layout`.
-- ============================================================

CREATE TABLE posts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  post_type TEXT NOT NULL DEFAULT 'page', -- 'page', 'blog_post', ... new types need no schema change
  route TEXT NOT NULL,        -- e.g. '/', '/about', '/blog/my-post'
  title TEXT NOT NULL,
  layout JSONB NOT NULL,
  meta_title TEXT,
  meta_description TEXT,
  og_image_id UUID REFERENCES media_assets(id),
  status TEXT NOT NULL DEFAULT 'draft', -- draft, published
  published_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, route)
);
CREATE INDEX posts_type_status_idx ON posts (tenant_id, post_type, status);
ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
ALTER TABLE posts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON posts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE templates (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  template_type TEXT NOT NULL, -- 'product', 'product_archive', 'cart', '404', 'order_confirmation',
                                -- 'blog_archive', 'search_results' (later types, no schema change needed)
  scope TEXT NOT NULL DEFAULT 'default', -- 'default' (applies to all), or a category_id/product_id
                                          -- for a future per-category override — no schema change needed
  layout JSONB NOT NULL,
  meta_title TEXT,
  meta_description TEXT,
  status TEXT NOT NULL DEFAULT 'draft',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, template_type, scope)
);
ALTER TABLE templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE templates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON templates
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE sections (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  section_type TEXT NOT NULL, -- 'header', 'footer', 'announcement_bar', 'popup'
  name TEXT NOT NULL,         -- merchant-facing label — matters once there are multiple popups
  layout JSONB NOT NULL,
  placement_rules JSONB,      -- popups/announcement bars only, e.g.
                               -- {"trigger": "exit_intent"|"delay"|"scroll",
                               --  "delay_seconds": 5, "pages": ["all"], "frequency": "once_per_session"}
  status TEXT NOT NULL DEFAULT 'draft',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE sections ENABLE ROW LEVEL SECURITY;
ALTER TABLE sections FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sections
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE redirects (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  from_path TEXT NOT NULL,
  to_path TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, from_path)
);
ALTER TABLE redirects ENABLE ROW LEVEL SECURITY;
ALTER TABLE redirects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON redirects
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Deferred, not in v1 schema (see docs/04-agent-build-spec.md Phase 6 notes):
--   post_revisions / template_revisions — undo-to-a-previous-save history
