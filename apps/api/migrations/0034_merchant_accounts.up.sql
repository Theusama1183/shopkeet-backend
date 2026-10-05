-- Multi-store merchant identity.
--
-- The merchant is the account (email + password); a store is a tenant it owns or
-- staffs. Login therefore authenticates an ACCOUNT, not a tenant, and must return
-- every store that account can open — which RLS cannot express, because at login
-- time the caller knows no tenant yet. So the identity lives in two account-scoped
-- tables that are deliberately RLS-free (same posture as `tenants`, which is
-- already RLS-free because subdomain resolution is public by design):
--
--   merchant_accounts      one row per login identity: email + password hash
--   merchant_account_stores the account's stores and its role in each
--
-- `merchant_users` (tenant-scoped, FORCE RLS) stays exactly as it is — it is the
-- tenant-side row admin routes read — and gains account_id so the two views of the
-- same membership cannot drift: merchant_account_stores has an FK into
-- merchant_users (account_id, tenant_id), so a mapping row cannot exist without a
-- real membership row, and role must be written to both in one transaction
-- (auth.addMembership does this — never write one without the other).
--
-- No merchant business data lives in either table: only identifiers and roles.
-- They are read by exactly two paths, both of which require proof of identity —
-- POST /auth/login (after CheckPassword succeeds on merchant_accounts) and the
-- store_pick/merchant token paths. A session-less caller cannot read them.

CREATE TABLE merchant_accounts (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email         TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Case-insensitive identity: you must not end up with two accounts for the same
-- address differing only in case, or login becomes ambiguous.
CREATE UNIQUE INDEX merchant_accounts_email_key ON merchant_accounts (lower(email));

ALTER TABLE merchant_users ADD COLUMN account_id UUID REFERENCES merchant_accounts(id);

-- Backfill: one account per distinct email. If the same email existed on two
-- tenants with different hashes, the earliest row wins and the later rows adopt
-- its password — that is the intended end state anyway (one password per
-- account), and pre-launch data has a single owner per store.
INSERT INTO merchant_accounts (email, password_hash)
SELECT DISTINCT ON (lower(email)) lower(email), password_hash
FROM merchant_users
ORDER BY lower(email), created_at;

UPDATE merchant_users u
SET account_id = a.id
FROM merchant_accounts a
WHERE lower(a.email) = lower(u.email);

ALTER TABLE merchant_users ALTER COLUMN account_id SET NOT NULL;

-- One membership row per (account, tenant); referenced by merchant_account_stores.
-- The old schema's UNIQUE (tenant_id, email) was case-SENSITIVE, so a tenant
-- could hold "Owner@x.com" and "owner@x.com" as two rows. After backfill both
-- resolve to one account, which would violate the constraint below. That state is
-- already incoherent — the same person, one login, two passwords — so fail loudly
-- with an actionable message instead of letting Postgres report a bare
-- duplicate-key error, and never silently drop a merchant row.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM merchant_users
    GROUP BY account_id, tenant_id HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION
      'cannot migrate: one account has two memberships in the same tenant (case-variant emails in merchant_users). Fix by deleting the stale row(s) before migrating.';
  END IF;
END
$$;

ALTER TABLE merchant_users
  ADD CONSTRAINT merchant_users_account_tenant_key UNIQUE (account_id, tenant_id);

CREATE TABLE merchant_account_stores (
  account_id UUID NOT NULL,
  tenant_id  UUID NOT NULL,
  role       TEXT NOT NULL DEFAULT 'owner', -- owner | staff
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (account_id, tenant_id),
  -- A mapping row may only exist where a real tenant-scoped membership exists.
  CONSTRAINT merchant_account_stores_membership_fk
    FOREIGN KEY (account_id, tenant_id)
    REFERENCES merchant_users (account_id, tenant_id) ON DELETE CASCADE
);

-- Backfill the mapping from the membership rows created above.
INSERT INTO merchant_account_stores (account_id, tenant_id, role, created_at)
SELECT account_id, tenant_id, role, created_at FROM merchant_users;

-- The one-time onboarding wizard runs after signup, before the merchant reaches
-- the dashboard. Signup creates the store provisionally and sets this false; the
-- wizard's POST /tenant/onboarding/complete sets it true and re-mints the session.
-- DEFAULT true so every already-configured store stays out of the wizard.
ALTER TABLE tenants
  ADD COLUMN onboarding_completed BOOLEAN NOT NULL DEFAULT true;