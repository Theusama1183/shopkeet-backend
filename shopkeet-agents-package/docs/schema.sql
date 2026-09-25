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
  logo_media_asset_id UUID REFERENCES media_assets(id), -- Phase 13: store logo
  default_currency TEXT NOT NULL DEFAULT 'usd',         -- Phase 13
  timezone TEXT NOT NULL DEFAULT 'UTC',                 -- Phase 13
  support_email TEXT,                                   -- Phase 13
  support_phone TEXT,                                   -- Phase 13
  tax_rate_percent INTEGER NOT NULL DEFAULT 0,          -- Phase 13: single flat tax rate
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
-- Product Options & Variants (Phase 8)
-- Every product always has at least one variant; a simple product gets a
-- single auto-created "Default" variant (no option values, no sku). Cart and
-- order lines reference the concrete variant; products.price_cents /
-- products.inventory_count are cached display values recomputed by the API as
-- MIN(active variant price) / SUM(active variant stock).
-- ============================================================

CREATE TABLE product_options (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  name       TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE product_options ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_options FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_options
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE product_option_values (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  option_id  UUID NOT NULL REFERENCES product_options(id),
  value      TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE product_option_values ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_option_values FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_option_values
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE product_variants (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  product_id      UUID NOT NULL REFERENCES products(id),
  sku             TEXT,
  price_cents     INTEGER NOT NULL,
  inventory_count INTEGER NOT NULL DEFAULT 0,
  weight_grams    INTEGER,
  status          TEXT NOT NULL DEFAULT 'active',          -- active, archived
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, sku)
);
ALTER TABLE product_variants ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_variants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_variants
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE product_variant_option_values (
  tenant_id      UUID NOT NULL REFERENCES tenants(id),
  variant_id     UUID NOT NULL REFERENCES product_variants(id),
  option_value_id UUID NOT NULL REFERENCES product_option_values(id),
  PRIMARY KEY (variant_id, option_value_id)
);
ALTER TABLE product_variant_option_values ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_variant_option_values FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_variant_option_values
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ============================================================
-- Cart & Inventory (Phase 4)
-- ============================================================

CREATE TABLE carts (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        UUID NOT NULL REFERENCES tenants(id),
  customer_session TEXT NOT NULL, -- opaque cookie-backed id; guest checkout supported
  discount_code    TEXT,          -- Phase 10: applied discount code (validated at checkout)
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
  product_id UUID NOT NULL REFERENCES products(id),      -- denormalized join key
  variant_id UUID NOT NULL REFERENCES product_variants(id),
  quantity   INTEGER NOT NULL CHECK (quantity > 0),
  UNIQUE (cart_id, variant_id) -- one line per variant in a cart; adds merge quantity
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
  shipping_address TEXT, -- deprecated flat column (Phase 9): kept as history, no longer written
  shipping_address_line1 TEXT,
  shipping_address_line2 TEXT,
  shipping_city TEXT,
  shipping_state TEXT,
  shipping_postal_code TEXT,
  shipping_country TEXT,
  shipping_method TEXT, -- Phase 9: snapshot of the shipping rate name at order time
  shipping_cost_cents INTEGER NOT NULL DEFAULT 0, -- Phase 9: applied cost (0 = free-over waived)
  discount_code TEXT, -- Phase 10: snapshot of the applied discount code at order time
  discount_cents INTEGER NOT NULL DEFAULT 0, -- Phase 10: computed discount applied to the order
  customer_id UUID REFERENCES customers(id), -- Phase 11: nullable — guest checkout still allowed
  tax_cents INTEGER NOT NULL DEFAULT 0,        -- Phase 13: tax snapshot at order time
  internal_note TEXT,                          -- Phase 13: merchant-only, never shown to customer
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
  product_id UUID NOT NULL REFERENCES products(id),      -- denormalized join key
  variant_id UUID NOT NULL REFERENCES product_variants(id),
  quantity INTEGER NOT NULL,
  unit_price_cents INTEGER NOT NULL -- snapshot: survives variant price edits
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

-- ============================================================
-- Shipping Zones & Rates (Phase 9)
-- Billing is per-destination: checkout requires a structured address plus a
-- shipping_rate_id and resolves the rate against the zone that covers the
-- destination (country in `countries`; a region-restricted zone also requires
-- the state to be in `regions` — without a state it never matches). The
-- resolved cost (rate_cents, waivable to 0 by free_over_cents) and rate name are
-- snapshotted into orders.shipping_cost_cents / orders.shipping_method at order
-- time; later edits never alter past orders.
-- ============================================================

CREATE TABLE shipping_zones (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  name TEXT NOT NULL,
  countries TEXT[] NOT NULL DEFAULT '{}',  -- ISO-3166 alpha-2 codes the zone covers
  regions TEXT[] NOT NULL DEFAULT '{}',    -- optional state/province codes; non-empty = restricted zone
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE shipping_zones ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipping_zones FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON shipping_zones
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE shipping_rates (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  zone_id UUID NOT NULL REFERENCES shipping_zones(id),
  name TEXT NOT NULL,              -- snapshot source for orders.shipping_method ("Standard", ...)
  rate_cents INTEGER NOT NULL,     -- flat cost applied at checkout
  free_over_cents INTEGER,         -- subtotal >= this waives the cost (NULL = never free)
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX shipping_rates_zone_idx ON shipping_rates (zone_id);

-- ============================================================
-- Discounts (Phase 10)
-- Admin-managed discount codes applied to a cart and snapshot into the order.
-- carts.discount_code records what the guest applied; orders snapshots the
-- applied code + computed discount_cents at checkout. Checkout re-validates the
-- code (status, date window, min subtotal, usage headroom) under FOR UPDATE and
-- increments times_used atomically, so a usage_limit=1 code is consumed exactly
-- once even under concurrent checkouts.
-- total_cents = subtotal - discount_cents + shipping_cost_cents.
-- ============================================================

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
  UNIQUE (tenant_id, code) -- codes are unique per tenant; normalized to uppercase
);
ALTER TABLE discounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE discounts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON discounts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- ============================================================
-- Customer Accounts (Phase 11)
-- Storefront shoppers register, log in, and save addresses; orders gain an
-- optional customer_id so a registered customer can see their history via
-- GET /customers/me/orders. Guest checkout stays fully supported (customer_id
-- NULL). Login/password both nullable on purpose: signup requires email+phone
-- +password today, but the columns model a future "order placed as a guest,
-- then claimed" flow without a schema change.
-- ============================================================

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
CREATE INDEX customer_addresses_customer_idx ON customer_addresses (customer_id);

-- ============================================================
-- Notifications (Phase 12)
-- Delivery log for customer communication (order confirmation, shipment
-- updates, welcome). Rows are written from the event subscribers; status is
-- 'sent' when the provider accepted the send, 'failed' when it didn't (a
-- failed send never fails the checkout that produced the event). RLS + FORCE
-- + OWNER keep tenants from seeing each other's delivery history.
-- ============================================================

CREATE TABLE notification_log (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  notification_type TEXT NOT NULL, -- 'order_confirmation', 'order_shipped', 'order_delivered', 'customer_welcome'
  recipient TEXT NOT NULL,         -- email or phone
  order_id UUID REFERENCES orders(id),
  status TEXT NOT NULL DEFAULT 'sent', -- sent, failed
  sent_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE notification_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_log FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_log
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE INDEX notification_log_tenant_sent_idx ON notification_log (tenant_id, sent_at);
