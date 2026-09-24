-- Phase 1: Tenants & Auth.
-- Mirrors docs/schema.sql (tenants, merchant_users). RLS policy is created in
-- the SAME migration as its table — the isolation guarantee ships with the
-- table, not as a follow-up (docs/02-tech-stack.md RLS rule).

CREATE TABLE tenants (
  id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name       TEXT NOT NULL,
  subdomain  TEXT UNIQUE NOT NULL,
  custom_domain TEXT UNIQUE,
  status     TEXT NOT NULL DEFAULT 'active', -- active | suspended
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE merchant_users (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     UUID NOT NULL REFERENCES tenants(id),
  email         TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL DEFAULT 'owner', -- owner | staff
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);

ALTER TABLE merchant_users ENABLE ROW LEVEL SECURITY;
-- The app's DB user creates these tables, so it owns them; without FORCE, RLS
-- is silently bypassed for the table owner/superuser and tenants would see
-- each other's rows. FORCE makes the policy bind to every session.
ALTER TABLE merchant_users FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON merchant_users
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
