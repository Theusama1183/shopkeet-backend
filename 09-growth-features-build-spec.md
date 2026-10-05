# Shopkeet — Growth & Merchandising Features (Phases 25–33)

Addendum to `04-agent-build-spec.md` / `07-expansion-build-spec.md` / `08-hardening-and-features-build-spec.md`, applied on top of the live Phase 0–24 backend. Same rules: work in order, `.agents/rules/*` still governs, each schema change is `ALTER`/new `CREATE TABLE` against the live database.

This round covers the app categories merchants reach for to actually grow revenue and operate at scale — bundles, upsells, a real affiliate program, custom fields, bulk data tools, and merchandising. Same framing as Phase 15–24: each one names the paid app category it replaces, verified against current (2026) app-store data.

---

## Phase 25 — Product Bundles & Quantity Breaks

Replaces **Releasit Bundles & Upsells** / **Kaching Bundles** (free tiers exist; paid tiers run to $30+/mo) — one of the highest-rated, most-installed app categories on Shopify specifically because it reliably lifts average order value.

```sql
CREATE TABLE bundles (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  name TEXT NOT NULL,
  type TEXT NOT NULL,                   -- 'fixed' (specific items), 'mix_and_match' (pick N from a pool)
  bundle_price_cents INTEGER,           -- flat price for the whole bundle...
  discount_percent INTEGER,             -- ...or a % off the sum of components. Exactly one is set.
  status TEXT NOT NULL DEFAULT 'draft',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE bundles ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bundles
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE bundle_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  bundle_id UUID NOT NULL REFERENCES bundles(id),
  product_id UUID NOT NULL REFERENCES products(id), -- for mix_and_match, one option in the pool
  quantity INTEGER NOT NULL DEFAULT 1               -- for 'fixed' bundles: how many included
);
ALTER TABLE bundle_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON bundle_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE quantity_breaks (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  min_quantity INTEGER NOT NULL,
  discount_percent INTEGER NOT NULL,
  UNIQUE (product_id, min_quantity)
);
ALTER TABLE quantity_breaks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON quantity_breaks
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE cart_items  ADD COLUMN bundle_id UUID REFERENCES bundles(id);
ALTER TABLE order_items ADD COLUMN bundle_id UUID REFERENCES bundles(id);
```

Adding a bundle to cart expands it into one `cart_items` row per component variant, all tagged with the same `bundle_id` — inventory still decrements per real variant at checkout (a bundle is a pricing construct, not a separate stock-keeping unit). Quantity breaks apply the highest qualifying `discount_percent` to a line once its quantity meets `min_quantity`.

**Endpoints:** `POST/GET/PATCH/DELETE /bundles[/:id]` (Admin), `GET /bundles` (Public, active only), `POST /cart/bundle` (Customer), `POST/GET/PATCH/DELETE /products/:id/quantity-breaks[/:id]` (Admin).

**Acceptance:** a bundle in cart prices at `bundle_price_cents`, not the sum of individual variant prices; checkout still correctly decrements each component's own stock.

---

## Phase 26 — Upsell, Cross-sell & Post-Purchase Offers

Replaces **ReConvert**/**Zipify OCU**-type apps (free tiers exist; paid from ~$15–30/mo).

```sql
CREATE TABLE product_recommendations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  recommended_product_id UUID NOT NULL REFERENCES products(id),
  type TEXT NOT NULL DEFAULT 'manual',  -- 'manual' (merchant-curated), 'auto' (see Phase 32)
  sort_order INTEGER NOT NULL DEFAULT 0,
  UNIQUE (product_id, recommended_product_id, type)
);
ALTER TABLE product_recommendations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_recommendations
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

**Post-purchase upsell** doesn't need a separate charge on Cash on Delivery — it's simply adding a line to the order that hasn't shipped yet: `POST /orders/:id/add-item` (Customer, from the order-confirmation screen, only while `status='pending'`) re-runs the Phase 4 stock check for the new item and recalculates `total_cents`.

**Endpoints:** `GET /products/:id/recommendations` (Public), `POST/DELETE /products/:id/recommendations` (Admin, manual curation), `POST /orders/:id/add-item` (Customer, post-purchase window only).

**Acceptance:** a post-purchase add succeeds while `status='pending'` and correctly updates the total and stock; it's rejected once the order has moved to `confirmed` or beyond.

---

## Phase 27 — Affiliate Program

Replaces **UpPromote** (free tier, then $29.99/$89.99/$199.99 per month) / **GOAFFPRO** / **Refersion** — a real affiliate system (tracking links, commissions, payouts), not just the basic referral-via-discount-code from Phase 20.

```sql
CREATE TABLE affiliates (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  name TEXT NOT NULL,
  email TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  code TEXT NOT NULL,                    -- tracking code, e.g. ?ref=CODE
  commission_percent INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending', -- pending, approved, rejected, suspended
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, email)
);
ALTER TABLE affiliates ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON affiliates
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE affiliate_commissions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  affiliate_id UUID NOT NULL REFERENCES affiliates(id),
  order_id UUID NOT NULL REFERENCES orders(id),
  commission_cents INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending', -- pending, approved (order delivered), paid, voided
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (affiliate_id, order_id)
);
ALTER TABLE affiliate_commissions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON affiliate_commissions
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE affiliate_payouts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  affiliate_id UUID NOT NULL REFERENCES affiliates(id),
  amount_cents INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'requested', -- requested, paid
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  paid_at TIMESTAMPTZ
);
ALTER TABLE affiliate_payouts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON affiliate_payouts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE orders ADD COLUMN affiliate_code TEXT; -- captured at checkout from a ?ref= param
```

**Deliberately kept simple, on purpose:** single-level commission only — no multi-level-marketing structure, even though some competing apps offer it. MLM tiers add real legal and abuse-surface complexity most small merchants don't need; add it later only if a specific merchant need shows up, not by default.

**Auth:** affiliates get a third JWT scope (`scope: "affiliate"`), distinct from both merchant and customer — an affiliate token must never pass either of those auth checks.

**Flow:** checkout with a valid `affiliate_code` creates a `pending` commission at the affiliate's current `commission_percent` of the order subtotal (not shipping/tax). The commission flips to `approved` only when the order reaches `delivered` (reuses the existing `order.paid`/delivery event) — this protects against paying commission on cancelled or returned orders. Payout itself is merchant-initiated and manual (mark `affiliate_payouts.status='paid'`) — consistent with there being no online payment rail yet.

**Endpoints:** `POST /affiliates/apply` (Public), `PATCH /affiliates/:id/status` (Admin), `GET /affiliates` (Admin), `GET /affiliates/me/dashboard`, `GET /affiliates/me/commissions`, `POST /affiliates/me/payout-request` (Affiliate auth), `PATCH /affiliate-payouts/:id` (Admin — mark paid).

**Acceptance:** an order placed via a valid affiliate link creates a `pending` commission at signup-time-percent; it becomes `approved` only on delivery, never before; an affiliate JWT is rejected by every merchant Admin and customer-scoped endpoint.

---

## Phase 28 — Custom Fields (Metafields)

Replaces metafields apps like **Accentuate Custom Fields** (free tier; paid $25–50/mo) — without this, merchants are stuck with only the fields already in the schema (material, ingredients, care instructions, custom spec sheets — anything not anticipated).

```sql
CREATE TABLE product_metafields (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  key TEXT NOT NULL,                  -- e.g. 'material', 'care_instructions'
  value TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'text',  -- 'text', 'number', 'boolean', 'json'
  UNIQUE (product_id, key)
);
ALTER TABLE product_metafields ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_metafields
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

**Endpoints:** `GET/PUT/DELETE /products/:id/metafields[/:key]` (Admin write); included in the existing public `GET /products/:id` response.

**Acceptance:** a merchant adds an arbitrary key (e.g. `material`) to one product and it appears on the public product response, with no schema change or deploy required for the next new key they invent.

---

## Phase 29 — Bulk Import / Export (CSV)

Replaces **Matrixify** (a genuinely expensive app, often $20–50+/mo) — and matters beyond cost: without this, a merchant with an existing 200-product catalog has no way onto the platform except typing every product in by hand.

No new tables. Large catalogs go through the existing Asynq job queue rather than a synchronous request:

**Endpoints:** `POST /products/import` (Admin, multipart CSV, returns a job id), `GET /products/import/:jobId` (Admin — status + a per-row success/error report), `GET /products/export` (Admin — CSV of the current catalog, synchronous is fine for export).

**Acceptance:** a CSV with one deliberately malformed row (missing a required field) still imports every valid row and reports the bad one by line number — not an all-or-nothing failure that discards good data because of one typo.

---

## Phase 30 — Product Feed (Google Shopping / Meta Catalog)

Replaces feed-management apps (CedCommerce, Facebook & Instagram channel-adjacent tools) — this is how a merchant actually gets found beyond their own storefront: paid ads and shopping tabs need a machine-readable catalog feed, and without one, merchants can't run this kind of advertising at all.

No new tables — generated from existing product/variant/image data.

**Endpoints:** `GET /feeds/google-shopping.xml` (Public, per-tenant), `GET /feeds/meta-catalog.csv` (Public, per-tenant) — standard field mappings (`id`, `title`, `price`, `availability`, `image_link`, `link`).

**Acceptance:** the generated feed validates against Google Merchant Center's required-field spec.

---

## Phase 31 — Smart (Rule-Based) Collections

Categories are currently manual-only (Phase 3) — every product has to be hand-assigned. Real stores need "all products under $20," "everything tagged 'sale'" to update itself.

```sql
CREATE TABLE smart_collection_rules (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  category_id UUID NOT NULL REFERENCES categories(id),
  field TEXT NOT NULL,     -- 'price', 'tag', 'status', ...
  operator TEXT NOT NULL,  -- 'lt', 'gt', 'eq', 'contains'
  value TEXT NOT NULL
);
ALTER TABLE smart_collection_rules ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON smart_collection_rules
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE categories ADD COLUMN is_smart BOOLEAN NOT NULL DEFAULT false;
```

A product matches a smart category when it satisfies **all** of that category's rules (AND logic — simplest correct version for v1). Recompute `product_categories` membership for smart categories whenever a product is saved, rather than at query time, so storefront listing stays fast.

**Acceptance:** a smart collection with rule `price < 2000` automatically includes a newly-created product priced at 1500, with no manual assignment.

---

## Phase 32 — Data-Driven Product Recommendations

Extends Phase 26's `product_recommendations` table with `type='auto'`. A scheduled job (weekly is enough at small-merchant order volume) computes, per product, which other products appear most often in the same order via `order_items`, and upserts the top matches as `type='auto'` rows.

**Acceptance:** `GET /products/:id/recommendations` returns auto-computed results once enough order history exists; when both manual and auto recommendations exist for the same product, manual ones take priority.

---

## Phase 33 — Customer Tags & Segments

```sql
CREATE TABLE customer_tags (
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  customer_id UUID NOT NULL REFERENCES customers(id),
  tag TEXT NOT NULL,
  PRIMARY KEY (customer_id, tag)
);
ALTER TABLE customer_tags ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_tags
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE discounts ADD COLUMN eligible_tag TEXT; -- NULL = anyone; else customer must carry this tag
```

**Endpoints:** `POST/DELETE /customers/:id/tags` (Admin), `GET /customers?tag=` (Admin — segment filter, e.g. for exporting a "VIP" list to target manually).

**Acceptance:** a discount with `eligible_tag='vip'` is rejected at checkout for a customer without that tag, even with a valid code.

---

## Deliberately not built, even though an app category exists for it

| Category | Why |
|---|---|
| Multi-level-marketing affiliate structures | Real legal/abuse-surface complexity most small merchants don't need — Phase 27 stays single-level on purpose. |
| Influencer content discovery/tracking (Modash-style) | A fundamentally different product (social listening + creator outreach) bolted onto commerce — better as a real third-party integration later than built in-house. |
| Fake urgency widgets ("X people viewing this," countdown timers with no real deadline) | Common app category, but a manipulative pattern by design — the honest version of "urgency" is a real low-stock count, which the existing `inventory_count` already supports without inventing anything fake. |
