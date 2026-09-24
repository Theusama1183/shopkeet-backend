-- Phase 6: Content & Page Builder. Mirrors docs/schema.sql posts/templates/
-- sections/redirects. The `layout` column on all three content entities stores a
-- Puck-compatible JSON tree directly (docs/04-agent-build-spec.md Phase 6) — the
-- Puck editor's onPublish output is saved verbatim, no transformation. Checkout
-- is deliberately NOT in the template system.
-- RLS policy ships in the same migration (docs/02-tech-stack.md); FORCE + owner
-- match the 0003/0004 model.

CREATE TABLE posts (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  post_type       TEXT NOT NULL DEFAULT 'page', -- 'page', 'blog_post', ... new types need no schema change
  route           TEXT NOT NULL,                -- e.g. '/', '/about', '/blog/my-post'
  title           TEXT NOT NULL,
  layout          JSONB NOT NULL,
  meta_title      TEXT,
  meta_description TEXT,
  og_image_id     UUID REFERENCES media_assets(id),
  status          TEXT NOT NULL DEFAULT 'draft', -- draft, published
  published_at    TIMESTAMPTZ,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, route)
);
CREATE INDEX posts_type_status_idx ON posts (tenant_id, post_type, status);

CREATE TABLE templates (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  template_type   TEXT NOT NULL, -- 'product', 'product_archive', 'cart', '404', 'order_confirmation', ...
  scope           TEXT NOT NULL DEFAULT 'default', -- per-category/product override later; no schema change
  layout          JSONB NOT NULL,
  meta_title      TEXT,
  meta_description TEXT,
  status          TEXT NOT NULL DEFAULT 'draft',
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, template_type, scope)
);

CREATE TABLE sections (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       UUID NOT NULL REFERENCES tenants(id),
  section_type    TEXT NOT NULL, -- 'header', 'footer', 'announcement_bar', 'popup'
  name            TEXT NOT NULL, -- merchant-facing label — matters once there are multiple popups
  layout          JSONB NOT NULL,
  placement_rules JSONB,         -- popups/announcement bars only:
                                 -- {"trigger": "exit_intent"|"delay"|"scroll",
                                 --  "delay_seconds": 5, "pages": ["all"], "frequency": "once_per_session"}
  status          TEXT NOT NULL DEFAULT 'draft',
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE redirects (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  from_path   TEXT NOT NULL,
  to_path     TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, from_path)
);

ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
ALTER TABLE posts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON posts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE templates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON templates
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE sections ENABLE ROW LEVEL SECURITY;
ALTER TABLE sections FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sections
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE redirects ENABLE ROW LEVEL SECURITY;
ALTER TABLE redirects FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON redirects
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE posts OWNER TO shopkeet_app;
ALTER TABLE templates OWNER TO shopkeet_app;
ALTER TABLE sections OWNER TO shopkeet_app;
ALTER TABLE redirects OWNER TO shopkeet_app;