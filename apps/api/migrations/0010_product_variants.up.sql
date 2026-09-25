-- Phase 8: Product Variants.
-- Separate tables for product options (e.g. "Size"), their values ("Small",
-- "Medium"), concrete variants (price/stock/SKU per option-value combination),
-- and the mapping table joining variants to their option values. Every product
-- always has at least one variant; a simple product gets a single auto-created
-- "Default" variant with no option values, so cart_items/order_items reference
-- variant_id uniformly (docs/07-expansion-build-spec.md Phase 8).
--
-- products.price_cents / products.inventory_count become cached display values
-- (MIN active variant price / SUM active variant stock) recomputed by the API
-- whenever a product's variants change; both columns stay populated (they hold
-- correct starting values after the backfill below).
--
-- RUN AS SUPERUSER (POSTGRES_USER shopkeet): the backfill below must INSERT/
-- UPDATE/DELETE rows on existing FORCE RLS tables (order_items, cart_items).
-- Postgres superusers bypass row security even with FORCE; the app role cannot
-- — turning row_security off only bypasses RLS for tables that are NOT FORCE
-- (FORCE tables then raise "query would be affected by row-level security"),
-- so applying this migration as shopkeet_app fails. Every table/constraint the
-- app serves is ALTERed back to ownership by shopkeet_app within this file.

SET LOCAL row_security = off;

-- ============================================================ product_options

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
ALTER TABLE product_options OWNER TO shopkeet_app;
CREATE INDEX product_options_product_idx ON product_options (product_id);

-- ======================================================== product_option_values

CREATE TABLE product_option_values (
  id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  option_id UUID NOT NULL REFERENCES product_options(id),
  value     TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE product_option_values ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_option_values FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_option_values
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE product_option_values OWNER TO shopkeet_app;
CREATE INDEX product_option_values_option_idx ON product_option_values (option_id);

-- ============================================================ product_variants

CREATE TABLE product_variants (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  product_id      UUID NOT NULL REFERENCES products(id),
  sku             TEXT,
  price_cents     INTEGER NOT NULL,
  inventory_count INTEGER NOT NULL DEFAULT 0,
  weight_grams    INTEGER,
  status          TEXT NOT NULL DEFAULT 'active', -- active, archived
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, sku)
);
ALTER TABLE product_variants ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_variants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_variants
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE product_variants OWNER TO shopkeet_app;
CREATE INDEX product_variants_product_idx ON product_variants (product_id);

-- ================================================ product_variant_option_values

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
ALTER TABLE product_variant_option_values OWNER TO shopkeet_app;

-- ============================================================ existing tables

-- cart_items / order_items now carry the concrete variant instead of the flat
-- product alone (product_id stays as a denormalized denormalized join key).
ALTER TABLE cart_items  ADD COLUMN variant_id UUID REFERENCES product_variants(id);
ALTER TABLE order_items ADD COLUMN variant_id UUID REFERENCES product_variants(id);

-- ============================================================ backfill

-- One default variant per existing product, carrying the flat product's price
-- and stock, no option values, no SKU.
INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
SELECT tenant_id, id, price_cents, inventory_count, status FROM products;

-- Real order history must survive: point every historical order line at its
-- product's newly-created default variant.
UPDATE order_items oi
SET variant_id = (SELECT pv.id FROM product_variants pv WHERE pv.product_id = oi.product_id LIMIT 1);

-- cart_items are transient session data — clear rather than backfill.
DELETE FROM cart_items;

-- Both new columns are now required going forward.
ALTER TABLE cart_items ALTER COLUMN variant_id SET NOT NULL;
ALTER TABLE order_items ALTER COLUMN variant_id SET NOT NULL;

-- A cart holds one line per (cart, variant); the same variant in different
-- option combinations is a distinct SKU/purchase.
ALTER TABLE cart_items DROP CONSTRAINT cart_items_cart_id_product_id_key;
ALTER TABLE cart_items ADD CONSTRAINT cart_items_cart_id_variant_id_key UNIQUE (cart_id, variant_id);