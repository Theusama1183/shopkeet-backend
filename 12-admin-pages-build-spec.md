# Shopkeet — Admin Pages Build Spec (Shopify Nav Mapping → Phases)

Research-grounded mapping of a real Shopify admin nav against what Shopkeet's backend (Phases 0–33) already supports, converted into page-build phases. Scope: **pages first, Puck/content-builder later** — these phases build real screens against the already-live API using the component library from `11-frontend-foundation-build-spec.md`; they don't touch the visual page-builder editor itself.

---

## Research findings on the less-obvious items

- **Purchase Orders vs. Transfers are different things, confirmed.** Purchase Orders record what was ordered from a *supplier* and what it cost — a restocking record. Transfers record inventory movement *between locations*. They're separate concepts that Shopify links together, not duplicates. Shopkeet has neither today.
- **Transfers specifically require multi-location inventory**, which is already on the deliberately-deferred list (`08-hardening-and-features-build-spec.md`) — no reason to revisit that call just because the nav item exists.
- **Segments are dynamic, saved filters, not static tags.** A segment is a saved query ("customers who spent over $X in the last 90 days") whose membership recalculates automatically as customer data changes — usable for both marketing and automated discount eligibility. This is meaningfully different from Phase 33's `customer_tags`, which are manually assigned and static. Worth a small, honest addition rather than pretending tags already cover it.
- **Metaobjects** are a merchant-defined custom-content-type system (merchants design their own schema, e.g. a "Recipe" type with its own fields) — a genuinely large feature (a schema builder, not just structured data). Shopkeet's `posts.post_type` (Phase 6) + `product_metafields` (Phase 28) already cover the common cases — specific content types and arbitrary key/value fields — without a full schema-builder. Not matching this one 1:1 is a deliberate call, not an oversight.
- **Live View** is real-time visitor analytics (who's on the store right now) — meaningfully different infrastructure (live/streaming data) from Phase 24's batch analytics endpoints. Flagged as a later phase, not built here.

---

## Mapping: Shopify nav → Shopkeet status

| Shopify nav item | Status |
|---|---|
| Home | New page — dashboard overview, built on Phase 24 analytics endpoints |
| Orders | ✅ Full backend (Phase 5) — needs a page |
| Orders → Drafts | ✅ Phase 15 (`source='draft'`) — needs a page |
| Orders → Abandoned checkouts | ✅ Data exists (Phase 17 `carts`) — needs a page (no dedicated endpoint yet, see Phase B below) |
| Products | ✅ Phase 3 + 8 (variants) — needs a page |
| Products → Collections | ✅ Phase 3 (manual) + Phase 31 (smart) — needs a page |
| Products → Inventory | ✅ Data exists on `product_variants` — needs a dedicated bulk-view/adjust page (no dedicated endpoint yet) |
| Products → Purchase orders | ❌ **Genuine backend gap** — see below |
| Products → Transfers | **Excluded** — requires multi-location, already deferred |
| Products → Gift cards | ✅ Phase 18 — needs a page |
| Customers | ✅ Phase 11 — needs a page |
| Customers → Segments | ⚠️ **Partial gap** — static tags exist (Phase 33), dynamic saved filters don't — see below |
| Customers → Companies | **Excluded** — B2B/wholesale, already deferred |
| Growth | New page — a UI home for Phases 17 (cart recovery), 20 (loyalty), 27 (affiliates) |
| Discounts | ✅ Phase 10 + 21 — needs a page |
| Content → Pages, Templates | ✅ Phase 6 — needs list/basic-edit pages (visual Puck editor is later) |
| Content → Blog posts | ✅ Phase 6 (`post_type='blog_post'`) — needs a page |
| Content → Files | ✅ Phase 2 (R2 media) — needs a media-library page |
| Content → Menus | Likely already covered by the `header`/`footer` sections' own layout (Phase 6) — revisit only if a dedicated reusable-menu-outside-sections need shows up |
| Content → Metaobjects | **Excluded** — covered at lighter weight by `post_type` + metafields, see above |
| Analytics → Reports | ✅ Phase 24 — needs a page |
| Analytics → Live View | **Deferred** — real-time infra, different problem than Phase 24's batch analytics |
| Sales channels → Online Store | Maps to the storefront itself, not an admin page |
| Sales channels → Point of Sale, Devices, Register sessions, (channel) Settings | **Excluded entirely** — retail/POS hardware, out of scope per `01-mission.md` |
| Settings | ✅ Phase 1 (staff) + 9 (shipping) + 13 (store settings) — needs a consolidated page with sub-sections |

---

## New backend work required before its page can be real (not UI-only)

### Purchase Orders — small, self-contained addition

```sql
CREATE TABLE suppliers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  name TEXT NOT NULL,
  email TEXT,
  phone TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE suppliers ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON suppliers
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE purchase_orders (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  supplier_id UUID NOT NULL REFERENCES suppliers(id),
  status TEXT NOT NULL DEFAULT 'draft', -- draft, ordered, partially_received, received, cancelled
  notes TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE purchase_orders ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON purchase_orders
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE purchase_order_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  purchase_order_id UUID NOT NULL REFERENCES purchase_orders(id),
  variant_id UUID NOT NULL REFERENCES product_variants(id),
  quantity_ordered INTEGER NOT NULL,
  quantity_received INTEGER NOT NULL DEFAULT 0,
  unit_cost_cents INTEGER NOT NULL
);
ALTER TABLE purchase_order_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON purchase_order_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

`PATCH /purchase-orders/:id/receive` — marks line items received (partial allowed, matching Shopify's own partial-delivery behavior) and **increments the variant's `inventory_count`** — this is the one place purchase orders touch existing Phase 8 data. No transfer concept needed — receiving just adds stock directly at the one location Shopkeet has.

`POST/GET/PATCH /suppliers[/:id]`, `POST/GET/PATCH /purchase-orders[/:id]`, `PATCH /purchase-orders/:id/receive` (all Admin).

**Acceptance:** receiving a PO line item increments the correct variant's stock by exactly the received quantity, not the ordered quantity, when it's a partial delivery.

### Segments — small addition, deliberately simpler than ShopifyQL

```sql
CREATE TABLE customer_segments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  name TEXT NOT NULL,
  filter JSONB NOT NULL, -- structured, not free-text query: e.g. {"total_spent_gte": 50000, "orders_count_gte": 3, "tag": "vip"}
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE customer_segments ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_segments
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

Membership is computed at read time from the `filter` JSON against `customers`/`orders`/`customer_tags` — not stored/materialized, so it's always current by construction (no sync job to get wrong). A **structured filter**, not a free-text query language — keeps this buildable in a day instead of weeks, and is still genuinely useful for the common cases (spend threshold, order count, has-tag).

`POST/GET/PATCH/DELETE /segments[/:id]` (Admin), `GET /segments/:id/customers` (Admin — resolves current membership).

**Acceptance:** a segment with `{"total_spent_gte": 50000}` correctly includes a customer the moment their cumulative order total crosses that threshold, with no manual recompute step.

---

## Page-build phases

### Phase A — Auth + Admin Shell (prerequisite if not already built)
Merchant login/signup against Phase 1's `/auth/*`; session handling (JWT storage, protected-route redirect); the shell itself — `Sidebar` nav (Home, Orders, Products, Customers, Growth, Discounts, Content, Analytics, Settings — the excluded POS-related items simply aren't in the nav at all, not hidden/disabled) + topbar, using components from `11-frontend-foundation-build-spec.md`.

### Phase B — Orders
`Home` (dashboard overview — key stats via `StatCard`, built on Phase 24), `Orders` list (`DataTable`, filters for status/payment status), order detail (line items, status transitions, tracking fields from Phase 23, `internal_note`, invoice PDF link), `Drafts` (filtered `source='draft'` view + the `POST /orders/draft` creation flow), `Abandoned checkouts` (list of `carts` with `recovery_sent_at` set and no resulting order — needs one new read-only endpoint, `GET /carts/abandoned`, Admin).

### Phase C — Products
Product list + detail/edit (variants, options, images via `MediaPicker`, metafields from Phase 28), `Collections` (manual + smart, Phase 31's rule builder), `Inventory` (a dedicated bulk view across all variants with inline stock adjustment — needs one new endpoint, `GET /inventory`, Admin, joining variants+products for a flat sortable list), `Purchase orders` (against the new backend above), `Gift cards` (Phase 18).

### Phase D — Customers
List + detail (orders, addresses, loyalty balance, tags), `Segments` (against the new backend above). Companies intentionally absent from the nav.

### Phase E — Growth & Discounts
A `Growth` page consolidating what already exists but has no UI home yet: abandoned-cart recovery stats (Phase 17), loyalty ledger overview (Phase 20), affiliate program management — applications, commissions, payouts (Phase 27). `Discounts` list + create/edit (Phase 10 + 21, including the automatic/BOGO fields).

### Phase F — Content
`Pages` and `Templates` list + a basic structured-form editor (not the Puck visual canvas — that's explicitly later), `Blog posts` (same pattern, `post_type='blog_post'`), `Files` (media library browser against Phase 2's R2 endpoints). Menus and Metaobjects intentionally absent.

### Phase G — Analytics
`Reports` against Phase 24's endpoints (sales over time, top products, conversion) using `Recharts`. `Live View` intentionally deferred — flag it in the nav as "coming soon" or simply omit it rather than build a placeholder page.

### Phase H — Settings
Consolidated page with sub-sections: store details/currency/timezone/tax rate (Phase 13), shipping zones & rates (Phase 9), staff/team (Phase 1's `merchant_users`, role management). Sales-channel-style POS settings intentionally absent.

Store details: Your business name, address, and contact information.

Plan and permissions: Managing your Shopify subscription and staff accounts.

Payments: Setting up payment providers and managing payouts.

Checkout: Customizing your online checkout process and customer account settings.

Shipping and delivery: Managing how orders are shipped to customers.

Taxes and duties: Setting up how taxes are calculated and charged.

Locations: Managing the physical locations where you hold inventory and fulfill orders.

Notifications: Setting up automated emails for order confirmations, shipping updates, and more.

---

## Explicitly not building, and why (consistent with existing deferred lists)

| Item | Why |
|---|---|
| Transfers | Requires multi-location inventory — already deferred |
| Companies | B2B/wholesale — already deferred |
| Point of Sale, Devices, Register sessions, channel Settings | Retail/POS hardware — out of scope per the mission doc, not revisited just because the nav item exists |
| Metaobjects (full schema-builder version) | `post_type` + metafields already cover the common cases at a fraction of the build cost |
| Live View | Real-time infra is a different problem from everything else in this file — a real later phase, not a stub now |
