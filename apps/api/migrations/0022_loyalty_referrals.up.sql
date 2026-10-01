-- Phase 20: Loyalty & Referrals (docs/08-hardening-and-features-build-spec.md).
-- customers.loyalty_points is the running balance; loyalty_ledger is the
-- append-only history (positive = earned, negative = redeemed). Points are
-- earned when an order reaches delivered (the existing order.paid event from
-- Phase 12) for its linked customer_id. Referrals reuse the discounts table: a
-- per-customer fixed_amount code whose customer_id points back at the referrer,
-- and the referrer earns points from a referred order at delivery. Merchants
-- tune the program via the two tenant knobs below (exposed through PATCH
-- /tenant/settings); 0 points_per_currency_unit = loyalty disabled for the
-- tenant. Pure DDL, runs as shopkeet_app like 0012/0013.

ALTER TABLE customers
  ADD COLUMN loyalty_points INTEGER NOT NULL DEFAULT 0;

CREATE TABLE loyalty_ledger (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  customer_id UUID NOT NULL REFERENCES customers(id),
  points      INTEGER NOT NULL,      -- positive = earned, negative = redeemed
  reason      TEXT NOT NULL,         -- 'order_placed', 'referral', 'redeemed', 'signup_bonus'
  order_id    UUID REFERENCES orders(id), -- the order that earned/redeemed against, when relevant
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE loyalty_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE loyalty_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON loyalty_ledger
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE loyalty_ledger OWNER TO shopkeet_app;
CREATE INDEX loyalty_ledger_customer_idx ON loyalty_ledger (customer_id, created_at DESC);

-- Exactly-once guards: an order may earn 'order_placed' once and pay out a
-- 'referral' once, even if the order.paid event were ever re-emitted.
CREATE UNIQUE INDEX loyalty_ledger_order_placed_once
  ON loyalty_ledger (order_id) WHERE order_id IS NOT NULL AND reason = 'order_placed';
CREATE UNIQUE INDEX loyalty_ledger_referral_once
  ON loyalty_ledger (order_id) WHERE order_id IS NOT NULL AND reason = 'referral';

ALTER TABLE tenants
  ADD COLUMN loyalty_points_per_currency_unit INTEGER NOT NULL DEFAULT 0, -- 0 = loyalty disabled for this tenant
  ADD COLUMN loyalty_redemption_rate INTEGER NOT NULL DEFAULT 100;        -- points per 1 currency unit of discount

-- Referrals: the discount row carries the referrer's customer_id. The existing
-- UNIQUE (tenant_id, code) keeps referral codes unique per tenant.
ALTER TABLE discounts ADD COLUMN customer_id UUID REFERENCES customers(id);