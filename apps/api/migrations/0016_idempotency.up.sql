-- Phase 14: Reliability hardening — idempotency keys.
-- A network retry or a double-tapped "Place Order" currently creates two
-- orders. This table stores the (tenant, endpoint, key) triplet from the
-- Idempotency-Key request header plus the cached successful response, so a
-- retry returns the stored response without re-running the handler. Rows are
-- purged after 24h by a scheduled Asynq job (created_at index).
--
-- RLS + FORCE + OWNER keep tenants from seeing each other's keys — but that
-- also means the app role cannot sweep the table: FORCE RLS still applies to
-- the owner, and row_security = off only bypasses RLS for tables that are NOT
-- FORCE (0010 documents the same rule). The maintenance DELETE therefore runs
-- through a SECURITY DEFINER function owned by a superuser role, which may
-- bypass FORCE RLS; shopkeet_app is granted EXECUTE. The function is
-- deliberately narrow (one DELETE with a bound cutoff) so the escalation is
-- contained (search_path pinned to avoid search-path hijacking).
--
-- RUN AS SUPERUSER (POSTGRES_USER shopkeet), exactly like 0010: only a
-- superuser can own a SECURITY DEFINER function that bypasses FORCE RLS. The
-- table/function ownership is handed to shopkeet_app within this file.

CREATE TABLE idempotency_keys (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  key TEXT NOT NULL,                -- client-supplied Idempotency-Key value
  endpoint TEXT NOT NULL,           -- e.g. 'POST /checkout'
  response_status INTEGER,          -- cached 2xx response; NULL while in flight
  response_body JSONB,              -- cached response payload
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, endpoint, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON idempotency_keys
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);

-- Maintenance sweep: deletes idempotency rows older than the cutoff and
-- returns how many were removed. SECURITY DEFINER + superuser ownership lets
-- it cross all tenants; pinned search_path keeps it deterministic.
CREATE OR REPLACE FUNCTION purge_idempotency_keys(cutoff timestamptz)
RETURNS bigint
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
AS $$
  WITH deleted AS (
    DELETE FROM idempotency_keys WHERE created_at < cutoff RETURNING id
  )
  SELECT count(*) FROM deleted;
$$;

ALTER TABLE idempotency_keys OWNER TO shopkeet_app;
GRANT EXECUTE ON FUNCTION purge_idempotency_keys(timestamptz) TO shopkeet_app;