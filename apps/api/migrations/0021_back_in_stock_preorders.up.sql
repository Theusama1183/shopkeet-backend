-- Phase 19: Pre-orders & Back-in-Stock Alerts (docs/08-hardening-and-features-build-spec.md).
-- A variant may opt into preorders (allow_preorder), letting checkout sell it
-- against zero/insufficient stock; such lines are marked is_preorder and skip
-- the inventory decrement. Customers can also subscribe to an out-of-stock,
-- non-preorderable variant and get one email exactly once when a merchant PATCH
-- restocks it (inventory moves 0 -> positive). Subscriptions carry
-- tenant_id + FORCE RLS + OWNER like every other tenant row, and a unique on
-- (tenant_id, variant_id, lower(email)) stops duplicate signups for the same
-- address regardless of case.

ALTER TABLE product_variants
  ADD COLUMN allow_preorder   BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN preorder_ships_at TIMESTAMPTZ;

ALTER TABLE order_items ADD COLUMN is_preorder BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE back_in_stock_subscriptions (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id),
  variant_id UUID NOT NULL REFERENCES product_variants(id),
  email      TEXT NOT NULL,
  notified_at TIMESTAMPTZ,               -- set exactly once after the first mail
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Expression constraints aren't allowed in a table-level UNIQUE list, so the
-- case-insensitive dedupe is a plain unique index instead.
CREATE UNIQUE INDEX back_in_stock_subscriptions_email_key
  ON back_in_stock_subscriptions (tenant_id, variant_id, lower(email));
ALTER TABLE back_in_stock_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE back_in_stock_subscriptions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON back_in_stock_subscriptions
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE back_in_stock_subscriptions OWNER TO shopkeet_app;