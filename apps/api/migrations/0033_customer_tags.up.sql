-- Phase 33: Customer Tags & Segments. Merchants tag customers (vip, wholesale,
-- newsletter) and gate discount codes on a required tag (discounts.eligible_tag)
-- so a code only applies at checkout when the customer carries the tag. RLS
-- contract uses the NULLIF-wrapped policy from migration 0029.
CREATE TABLE customer_tags (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
  tag         TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, customer_id, tag)
);

ALTER TABLE customer_tags ENABLE ROW LEVEL SECURITY;
ALTER TABLE customer_tags FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_tags
  USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);

ALTER TABLE customer_tags OWNER TO shopkeet_app;

CREATE INDEX customer_tags_tag_idx ON customer_tags (tenant_id, tag, created_at);

-- Discount codes may require a customer tag to apply (checked at checkout).
ALTER TABLE discounts ADD COLUMN eligible_tag TEXT;