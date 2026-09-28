-- Phase 18: Gift Cards.
-- Merchant-issued store credit: a code with a balance a customer applies at
-- checkout, claiming exactly what they owe (floored at 0, unused balance stays
-- on the card). carts records the code a guest applied; orders snapshots the
-- applied code + gift_card_cents at checkout (never re-opened on later balance
-- changes). FORCE RLS + OWNER in the same migration like every tenant table.

-- =========================================================== gift_cards

CREATE TABLE gift_cards (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            UUID NOT NULL REFERENCES tenants(id),
  code                 TEXT NOT NULL,
  initial_balance_cents INTEGER NOT NULL,        -- what was issued
  balance_cents        INTEGER NOT NULL,         -- what's left to spend
  status               TEXT NOT NULL DEFAULT 'active', -- active, disabled
  expires_at           TIMESTAMPTZ,              -- NULL = never expires
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (initial_balance_cents > 0),
  CHECK (balance_cents >= 0),
  CHECK (status IN ('active', 'disabled')),
  UNIQUE (tenant_id, code)
);
ALTER TABLE gift_cards ENABLE ROW LEVEL SECURITY;
ALTER TABLE gift_cards FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON gift_cards
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE gift_cards OWNER TO shopkeet_app;

-- ============================================================ existing tables

ALTER TABLE carts  ADD COLUMN gift_card_code TEXT;

ALTER TABLE orders ADD COLUMN gift_card_code  TEXT,
                   ADD COLUMN gift_card_cents INTEGER NOT NULL DEFAULT 0;