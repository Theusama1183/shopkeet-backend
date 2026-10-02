-- Phase 27: Affiliate Program (docs/09-growth-features-build-spec.md).
-- Single-level affiliate tracking. A merchant approves an affiliate and sets
-- their commission_percent; a checkout that arrives via ?ref=CODE snapshots
-- orders.affiliate_code and books a pending commission at commission_percent of
-- the order's discounted goods subtotal (shipping/tax are excluded). The
-- commission flips to approved only when the order reaches delivered (the
-- existing order.paid event), so cancelled/returned orders never pay out.
-- Payouts are merchant-initiated and manual. Each table keeps the established
-- RLS contract: tenant_id + ENABLE/FORCE ROW LEVEL SECURITY + same-migration
-- NULLIF-wrapped policy (migration 0029) + OWNER, so a stale '' custom GUC in a
-- pooled session reads as NULL (fail-closed zero rows) instead of erroring.

-- ============================================================ affiliates

CREATE TABLE affiliates (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id),
  name               TEXT NOT NULL,
  email              TEXT NOT NULL,
  password_hash      TEXT NOT NULL,
  code               TEXT NOT NULL,                    -- tracking code, e.g. ?ref=CODE
  commission_percent INTEGER NOT NULL DEFAULT 10,      -- set by the merchant, usually before approving
  status             TEXT NOT NULL DEFAULT 'pending',  -- pending, approved, rejected, suspended
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (status IN ('pending', 'approved', 'rejected', 'suspended')),
  CHECK (commission_percent BETWEEN 0 AND 100),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, email)
);
ALTER TABLE affiliates ENABLE ROW LEVEL SECURITY;
ALTER TABLE affiliates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON affiliates
  USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
ALTER TABLE affiliates OWNER TO shopkeet_app;

CREATE INDEX affiliates_tenant_status_idx ON affiliates (tenant_id, status, created_at);

-- ============================================================ affiliate_commissions

-- One row per referred order, created at checkout with a snapshot of the
-- commission. status: 'pending' at checkout, 'approved' when the order is
-- delivered (order.paid), 'paid' when included in a paid-out payout,
-- 'voided' reserved for reversal.
CREATE TABLE affiliate_commissions (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        UUID NOT NULL REFERENCES tenants(id),
  affiliate_id     UUID NOT NULL REFERENCES affiliates(id) ON DELETE CASCADE,
  order_id         UUID NOT NULL REFERENCES orders(id),
  commission_cents INTEGER NOT NULL,
  status           TEXT NOT NULL DEFAULT 'pending', -- pending, approved, paid, voided
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (commission_cents >= 0),
  CHECK (status IN ('pending', 'approved', 'paid', 'voided')),
  UNIQUE (affiliate_id, order_id)
);
ALTER TABLE affiliate_commissions ENABLE ROW LEVEL SECURITY;
ALTER TABLE affiliate_commissions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON affiliate_commissions
  USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
ALTER TABLE affiliate_commissions OWNER TO shopkeet_app;

CREATE INDEX affiliate_commissions_affiliate_idx ON affiliate_commissions (affiliate_id, status, created_at);
CREATE INDEX affiliate_commissions_order_idx ON affiliate_commissions (order_id);

-- ============================================================ affiliate_payouts

-- An affiliate requests a payout of whatever they have currently approved; the
-- merchant marks it paid once they transfer the money, which flips that
-- affiliate's approved commissions to paid.
CREATE TABLE affiliate_payouts (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    UUID NOT NULL REFERENCES tenants(id),
  affiliate_id UUID NOT NULL REFERENCES affiliates(id) ON DELETE CASCADE,
  amount_cents INTEGER NOT NULL,
  status       TEXT NOT NULL DEFAULT 'requested', -- requested, paid
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  paid_at      TIMESTAMPTZ,
  CHECK (amount_cents > 0),
  CHECK (status IN ('requested', 'paid'))
);
ALTER TABLE affiliate_payouts ENABLE ROW LEVEL SECURITY;
ALTER TABLE affiliate_payouts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON affiliate_payouts
  USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
ALTER TABLE affiliate_payouts OWNER TO shopkeet_app;

CREATE INDEX affiliate_payouts_affiliate_idx ON affiliate_payouts (affiliate_id, status, requested_at);

-- ============================================================ order link

-- Captured at checkout from a ?ref=CODE param (or the body's affiliate_code).
-- NULL when the order came in without a valid affiliate link.
ALTER TABLE orders ADD COLUMN affiliate_code TEXT;