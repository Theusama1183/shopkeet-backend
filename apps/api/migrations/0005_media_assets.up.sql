-- Phase 2: Media library — Cloudflare R2 (metadata only; bytes go directly
-- client <-> R2 via presigned URLs, never through the API).
-- Mirrors docs/schema.sql media_assets. RLS policy ships in the same
-- migration as the table (docs/02-tech-stack.md RLS rule).

CREATE TABLE media_assets (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    UUID NOT NULL REFERENCES tenants(id),
  r2_key       TEXT NOT NULL,          -- object key in the R2 bucket, e.g. "{tenant_id}/{uuid}.jpg"
  url          TEXT NOT NULL,          -- public URL (R2_PUBLIC_URL + "/" + r2_key)
  content_type TEXT NOT NULL,
  size_bytes   INTEGER NOT NULL,
  alt_text     TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (r2_key)
);

ALTER TABLE media_assets ENABLE ROW LEVEL SECURITY;
-- Same rationale as merchant_users: bind RLS to the table owner too so the
-- app role (shopkeet_app, non-superuser owner) is never silently exempt.
ALTER TABLE media_assets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON media_assets
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- App role owns it, matching 0003's ownership model; default grants from 0004
-- cover anything this migration misses.
ALTER TABLE media_assets OWNER TO shopkeet_app;