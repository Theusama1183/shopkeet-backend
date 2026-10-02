# Shopkeet — Project Status & Complete State (Phases 1–30)

**Last verified:** 2026-10-03 against live prod (Phases 28–30 acceptance on `shopkeet_app` + DB 31)
**DB version:** 31 (migrations 0001–0031 applied, `schema_migrations = 31 | dirty=f`)
**Deployment:** live on `https://api.shopkeet.com` (Coolify-managed, healthz `200`) — deploy is not automatic on git push
**Status:** Phases 1–27 complete, deployed, and verified live; **Phases 28–30 (metafields / bulk CSV import+export / product feeds) built, accepted live, and deployed.**

> Purpose of this file: one page a fresh Claude/agent can read to know **exactly how far
> the project has gone** — every phase, every table + field, what succeeded, what broke and
> was fixed, what is still caveated, and the exact commands to deploy/migrate/smoke/clean up.
> Phase-by-phase narrative lives in `SHOPKEET_API_PHASES_1_TO_13.md`; this is the snapshot.

---

## 1. One-paragraph summary

Shopkeet is a single-module Go monolith (Fiber + pgx v5 + Redis) delivering a
multi-tenant e-commerce platform *as a bunch of SaaS-app replacements* — Wishlist Plus,
LifeTimely/Triple Whale, AfterShip/Sufio, Bold Discounts, LoyaltyLion, etc. Shipping one
phase at a time. Through Phase 24 it has built: tenant auth + RLS isolation, media/R2,
catalog with variants, cart + inventory (Redis reservation), COD checkout + orders,
content/page-builder, observability, shipping zones/rates, discounts (manual and
automatic), customer accounts, notifications (Resend), store settings/tax, platform
hardening (rate limit, idempotency, cache), draft orders + returns, product reviews,
abandoned-cart recovery emails, gift cards, pre-orders/back-in-stock, loyalty/referrals,
wishlist, order tracking + server-side invoice PDF, a merchant analytics dashboard,
product bundles + quantity breaks, merchant-curated product recommendations
with a post-purchase order upsell, (Phase 27) a single-level affiliate program
with commissions paid on delivered orders and manual merchant-paid payouts,
(Phase 28) per-product metafields/custom fields, (Phase 29) bulk CSV product
import/export, and (Phase 30) Google Shopping + Meta product feeds.
Every tenant-scoped table ships with `tenant_id` + `ENABLE/FORCE ROW LEVEL SECURITY` +
a `tenant_isolation` policy (NULLIF-wrapped since `0029`, see below) + `OWNER TO shopkeet_app`
in **the same migration**.
Prod runs 41 tables / migration 30; all acceptance tests PASS.

---

## 2. Completeness matrix (phase → migration → status)

| Phase | Built | Migration | Test | Status |
|-------|-------|-----------|------|--------|
| 1 | Auth (tenants, merchant_users, signup/login, JWT scopes) | `0002` | `TestTenantRLSIsolation` | ✅ deployed |
| 2 | Media / Cloudflare R2 (presigned URLs, asset metadata) | `0005` | `TestMediaRLSIsolation` | ✅ deployed |
| 3 | Catalog (categories, products, images, search) | `0006` | `TestCatalogRLSIsolation` | ✅ deployed |
| 4 | Cart & Inventory (carts, cart_items + Redis reserve) | `0007` | `TestCartRLSIsolation`, `TestRedisReserver` | ✅ deployed |
| 5 | Checkout & Orders (COD, order_items, status flow) | `0008` | `TestOrdersRLSIsolation` | ✅ deployed |
| 6 | Content & Page Builder (posts, templates, sections, redirects) | `0009` | `TestContentRLSIsolation` | ✅ deployed |
| 7 | Observability (metrics, error shape, rate limiting) | — | `TestErrorShape`, `TestMetricsExposition` | ✅ deployed |
| 8 | Product Variants (options/values, variant inventory) | `0010` | `TestVariantsRLSIsolation` | ✅ deployed |
| 9 | Shipping (zones, rates, per-order address block) | `0011` | `TestShippingRLSIsolation` | ✅ deployed |
| 10 | Discounts (flat/percent, min-subtotal, times_used) | `0012` | `TestDiscountsAcceptance` | ✅ deployed |
| 11 | Customer Accounts (customers, addresses, `/customers/me`) | `0013` | `TestCustomersRLSIsolation` | ✅ deployed |
| 12 | Notifications (notification_log, Resend prod) | `0014` | — | ✅ deployed |
| 13 | Store Settings, Order Notes & Tax (tenants settings, tax_cents) | `0015` | — | ✅ deployed |
| 14 | Platform hardening (rate limit, idempotency keys, Redis cache) | `0016` | `TestIdempotencyReplay`, `TestIdempotencyPurgeExpired`, `TestRateLimitWindow`, `TestRedisSetGetRoundTrip`, `TestRedisDelPrefix`, `TestInvalidateProduct`, `TestProductCacheAside` | ✅ deployed |
| 15 | Merchant Ops (draft orders `source='draft'`, returns/re-stock) | `0017` | `TestDraftOrderDecrementsStock`, `TestReturnRestocksCorrectVariant` | ✅ deployed |
| 16 | Product Reviews (rating aggregate, verified) | `0018` | `TestReviewLifecycleAndVerified`, `TestRejectAndDeleteRecompute` | ✅ deployed |
| 17 | Abandoned Cart Recovery + lifecycle emails | `0019` | `TestCartRecoverySweep`, `TestSendCartAbandoned` | ✅ deployed |
| 18 | Gift Cards (balance, claim, checkout snapshot, double-spend guard) | `0020` | `TestGiftCardsAdmin`, `TestResolveValidations`, `TestTenantScopeWithoutRLS`, `TestGiftCardCheckoutSnapshot`, `TestGiftCardConcurrentDoubleSpend` | ✅ deployed |
| 19 | Pre-orders & Back-in-stock Alerts | `0021` | `TestPreorderCheckout`, `TestNotifyMe`, `TestVariantPreorderFields`, `TestDeliverBackInStockExactlyOnce` | ✅ deployed |
| 20 | Loyalty & Referrals | `0022` | `TestLoyaltyReferrals` | ✅ deployed |
| 21 | Advanced & Automatic Discounts (`requires_code=false`) | `0023` | `TestAutomaticDiscounts` | ✅ deployed |
| 22 | Wishlist | `0024` | `TestWishlistFlow` | ✅ deployed |
| 23 | Order Tracking + Invoice PDF | `0025` | `TestOrderTrackingAndInvoicePDF`, `TestInvoiceTotalsMath` | ✅ deployed |
| 24 | Storefront Analytics (sales / top-products / conversion) | `0026` | `TestAnalyticsReconciliation`, `TestParsePeriodAndLimit` | ✅ deployed |
| 25 | Product Bundles (fixed flat / mix-and-match % off) + Quantity Breaks | `0027` | `TestBundlesAcceptance` | ✅ deployed |
| 26 | Upsell & Cross-sell Recommendations + Post-Purchase add-item | `0028` | `TestRecommendationsAcceptance` | ✅ deployed |
| 27 | Affiliate Program (apply/approve, `?ref=` commissions, manual payouts) | `0030` | `TestAffiliateProgram` | ✅ deployed |
| 28 | Metafields / Custom Fields (`product_metafields`, admin CRUD, embedded public) | `0031` | `TestProductMetafields` | ✅ deployed |
| 29 | Bulk CSV Import (async job + report) & Export | — (no migration) | `TestProductCSVImportExport` | ✅ deployed |
| 30 | Product Feeds (Google Shopping XML + Meta Catalog CSV) | — (no migration) | `TestProductFeeds` | ✅ deployed |

All migrations applied on the VPS DB (`schema_migrations` = 31). The currently-deployed API image
covers everything through Phase 30.

---

## 3. Deployment & infrastructure (current state)

- **API** — one Go binary, Coolify-managed app (container `l6modsyezs1vlrv6ly1oqz4i-…`,
  listens on `:3001` internally), proxied by `coolify-proxy` (traefik) at
  `https://api.shopkeet.com`. **Deploy is not automatic on git push** (verified
  2026-10-02: pushing `a0b89bb` left the old `4a553ef` container running) — the image is
  released via the Coolify deploy trigger (`p21-deploy.ps1`).
- **Postgres** — manual container `shopkeet-postgres` (postgres:16-alpine), DB `shopkeet`,
  volume `infra_postgres_data`. **Not** a Coolify service (known hybrid, see
  `03-architecture.md §7`).
- **Redis** — manual container `shopkeet-redis` (redis:7-alpine). Cache/reservation only,
  never the source of truth.
- **Roles:** `shopkeet` (superuser — migrations/admin only) · `shopkeet_app` (non-superuser,
  `OWNER` of every tenant table + all sequences; what the API connects as for normal traffic).
- **RLS is genuinely FORCED in prod** (since 2026-09-28 — see issue #1 below).
- **JWT secret / DB URL / Coolify tokens:** in the tool-call chain / deploy script
  (`C:\Users\Usama\AppData\Local\Temp\opencode\p21-deploy.ps1`) — never commit secrets.
- Ondrops: VPS `ubuntu@13.61.125.59`, key `C:\Users\Usama\.ssh\shopkeet_key_pair.pem`,
  `ssh -i … -o StrictHostKeyChecking=no`.

---

## 4. Full schema — every table, every field (live dump 2026-10-02)

`tenant_id uuid NOT NULL REFERENCES tenants(id)` + `ENABLE RLS` + `FORCE RLS` +
`POLICY tenant_isolation … (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)`
+ `OWNER TO shopkeet_app` on **every** table below (all except `tenants` and
`schema_migrations`, which are RLS-free by design). The `NULLIF` wrap (migration
`0029`) turns the reset-to-`''` custom GUC after a transaction-local
`set_config` back into `NULL`, so an out-of-transaction query fails closed
(zero rows) instead of raising `22P02`.

**tenants** (root, no RLS): id, name, subdomain (UNIQUE), custom_domain (UNIQUE), status,
created_at, **logo_media_asset_id**, **default_currency**, **timezone**, **support_email**,
**support_phone**, **tax_rate_percent** (P13), **loyalty_points_per_currency_unit**,
**loyalty_redemption_rate** (P20).

**merchant_users** (P1): id, tenant_id, email, password_hash, role (`owner|staff`),
created_at. UNIQUE (tenant_id, email).

**media_assets** (P2): id, tenant_id, r2_key (UNIQUE), url, content_type, size_bytes,
alt_text, created_at.

**categories** (P3): id, tenant_id, name, slug. UNIQUE (tenant_id, slug).
**products** (P3): id, tenant_id, name, slug (UNIQUE tenant), description, price_cents,
currency, inventory_count, status (`draft|active|archived`), meta_title, meta_description,
search_vector TSVECTOR generated (English `to_tsvector`), created_at,
**rating_average NUMERIC(2,1)**, **rating_count INTEGER** (P16).
**product_metafields** (P28): id, tenant_id, product_id (FK), key, value (TEXT),
type (`string|number|boolean|json`), timestamps. UNIQUE (product_id, key).
**product_categories** (P3): tenant_id, product_id, category_id. PK (product_id, category_id).
**product_images** (P3): id, tenant_id, product_id, media_asset_id, sort_order. UNIQUE (product_id, media_asset_id).

**carts** (P4): id, tenant_id, customer_session (UNIQUE per tenant), created_at,
**discount_code** (P10), **customer_email**, **last_activity_at**, **recovery_sent_at** (P17),
**gift_card_code** (P18).
**cart_items** (P4): id, tenant_id, cart_id, product_id, **variant_id** (P8), quantity CHECK(>0).
UNIQUE (cart_id, variant_id).

**orders** (P5): id, tenant_id, customer_name, customer_phone, customer_email,
shipping_address (legacy one-line TEXT), payment_method (`cod`), payment_status
(`pending|paid|failed`), status (`pending|confirmed|shipped|delivered|cancelled`), total_cents,
currency, created_at, **shipping_address_line1/2, shipping_city/state/postal_code/country,
shipping_method, shipping_cost_cents** (P9), **discount_code, discount_cents** (P10),
**customer_id** (P11), **internal_note, tax_cents** (P13), **source** (`storefront|draft`, P15),
**gift_card_code, gift_card_cents** (P18), **tracking_number, tracking_carrier,
tracking_url** (P23), **affiliate_code** (P27 — snapshot of the code credited for the order).
**order_items** (P5): id, tenant_id, order_id, product_id, quantity, unit_price_cents,
**variant_id** (P8), **is_preorder** (P19).

**posts** (P6): id, tenant_id, post_type, route (UNIQUE tenant), title, layout JSONB (Puck),
meta_title, meta_description, og_image_id, status, published_at, updated_at.
**templates** (P6): id, tenant_id, template_type, scope (`default`, per-category reserved),
layout JSONB, meta_title, meta_description, status, updated_at. UNIQUE (tenant_type, scope).
**sections** (P6): id, tenant_id, section_type, name, layout JSONB, placement_rules JSONB,
status, updated_at.
**redirects** (P6): id, tenant_id, from_path (UNIQUE tenant), to_path, created_at.

**product_options** (P8): id, tenant_id, product_id, name, sort_order.
**product_option_values** (P8): id, tenant_id, option_id, value, sort_order.
**product_variants** (P8): id, tenant_id, product_id, sku (UNIQUE tenant), price_cents,
inventory_count, weight_grams, status, created_at, **allow_preorder, preorder_ships_at** (P19).
**product_variant_option_values** (P8): tenant_id, variant_id, option_value_id.
PK (variant_id, option_value_id).

**shipping_zones** (P9): id, tenant_id, name, countries ARRAY, regions ARRAY, created_at.
**shipping_rates** (P9): id, tenant_id, zone_id, name, rate_cents, free_over_cents, sort_order.

**discounts** (P10 + P21): id, tenant_id, code, type, value_percent, value_cents,
min_subtotal_cents, starts_at, ends_at, usage_limit, times_used, status, created_at,
**customer_id** (P20), **applies_to** (`order|shipping`, P21), **buy_quantity, get_quantity**
(reserved BOGO → 400), **requires_code** (P21, auto promo).

**customers** (P11): id, tenant_id, email (UNIQUE tenant), phone, password_hash, created_at,
**loyalty_points** (P20).
**customer_addresses** (P11): id, tenant_id, customer_id, label, address_line1/2, city,
state, postal_code, country, is_default.

**notification_log** (P12 + P19): id, tenant_id, notification_type (incl. `back_in_stock`,
`cart_abandoned`), recipient, order_id, status, sent_at. Type `back_in_stock` since P19.

**idempotency_keys** (P16): id, tenant_id, key, endpoint (UNIQUE tenant+endpoint+key),
response_status, response_body JSONB, created_at.

**returns, return_items** (P15): returns: id, tenant_id, order_id, reason, status, restock,
created_at, updated_at. return_items: id, tenant_id, return_id, order_item_id, quantity.

**product_reviews** (P16): id, tenant_id, product_id, customer_id, order_id, rating,
title, body, photo_media_asset_ids ARRAY, status, created_at. (Rating recomputed on
reject/delete; verified = bought order.)

**gift_cards** (P18): id, tenant_id, code (UNIQUE tenant), initial_balance_cents,
balance_cents, status, expires_at, created_at.

**back_in_stock_subscriptions** (P19): id, tenant_id, variant_id, email, notified_at,
created_at. UNIQUE (tenant_id, variant_id, lower(email)).

**loyalty_ledger** (P20): id, tenant_id, customer_id, points, reason (`order_placed` | `referral` | redeemed), order_id, created_at.
Partial unique indexes: `order_placed_once`, `referral_once` (one per order).

**wishlist_items** (P22): id, tenant_id, customer_id, product_id, created_at.
UNIQUE (customer_id, product_id).

**affiliates** (P27): id, tenant_id, name, email (UNIQUE tenant), code (UNIQUE tenant,
canonical — both `affiliate_code` body param and `?ref=` query match it), commission_percent
(NUMERIC), status (`pending|approved|suspended`), created_at.
**affiliate_commissions** (P27): id, tenant_id, affiliate_id, order_id, commission_cents,
status (`pending|approved|paid`), created_at. Snapshot rules: UNIQUE (affiliate_id, order_id)
`ON CONFLICT DO NOTHING`; `delivered` order event (`order.paid`) flips `pending→approved`;
merchant marking the payout `paid` flips `approved→paid`.
**affiliate_payouts** (P27): id, tenant_id, affiliate_id, amount_cents,
status (`requested|paid`), created_at. Request blocked while one is already `requested`;
`paid` covers all currently-`approved` commissions for that affiliate.

**schema_migrations**: version (BIGINT), dirty. Currently `31 | f`.

### Indexes worth knowing (beyond PKs/UNIQUEs)
- `products_search_idx` GIN on `search_vector`.
- `carts_recovery_scan_idx` partial: `(tenant_id, last_activity_at) WHERE customer_email IS NOT NULL AND recovery_sent_at IS NULL` (P17 sweep).
- `loyalty_ledger_customer_idx`, `loyalty_ledger_order_placed_once`, `loyalty_ledger_referral_once`.
- **Phase 24:** `orders_created_at_idx (tenant_id, created_at DESC)`,
  `order_items_order_idx (order_id)`, `carts_created_at_idx (tenant_id, created_at)`.

---

## 5. API surface (group summary)

| Area (Phase) | Key routes |
|---|---|
| Auth (1) | `POST /auth/signup`, `/auth/login` |
| Media (2) | `POST /media/upload-url`, `POST /media`, `GET /media`, `DELETE /media/:id` |
| Catalog (3, 8) | `GET /products` (public), `GET/POST/PATCH/DELETE /products/:id`, `/products/:id/images`, `/categories`, `/products/:id/variants` *(admin)*, `/products/:id/variants/:variantId/notify-me` *(public, P19)* |
| Cart (4, 17) | `GET?/POST /cart`, `PATCH/DELETE /cart/items/:id`, `POST /cart/discount` (P10), `POST /cart/email` (P17), `POST /cart/gift-card` (P18) |
| Checkout & Orders (5, 9, 13, 15, 23) | `POST /checkout`, `GET /orders/:id` (customer lookup), `GET /orders` (admin), `PATCH /orders/:id/status` (advance + tracking), `POST /orders` (draft, admin), `POST /orders/:id/returns`, `GET /orders/:id/invoice.pdf` (admin **or** customer `?phone=`) |
| Content (6) | `GET/POST/PATCH/DELETE /posts`, `/templates/:template_type` (PUT admin / GET public), `/sections`, `/redirects`, `/redirects/lookup?path=` |
| Platform (7, 14) | `GET /healthz`, `GET /metrics` |
| Shipping (9) | `/shipping-zones`, `/shipping-rates` (+ public rates fetch for checkout) |
| Discounts (10, 21) | `POST/GET/PATCH /discounts`, `/discounts/:id` |
| Customers (11, 20, 22) | `/customers/me`, `/customers/me/wishlist`, `/customers/me/loyalty`, `/customers/me/referral`, `POST /loyalty/redeem` |
| Reviews (16) | `POST /products/:id/reviews`, list/fetch, admin reject/delete |
| Settings (13) | `GET/PATCH /tenant/settings` |
| **Analytics (24)** | `GET /analytics/sales?period=7d\|30d\|90d`, `GET /analytics/top-products?period=&metric=quantity\|revenue&limit=`, `GET /analytics/conversion?period=` — all Merchant |
| **Affiliates (27)** | Public: `POST /affiliates/apply` (X-Tenant-ID), `POST /affiliates/login`; Merchant: `GET /affiliates`, `PATCH /affiliates/:id/status`, `PATCH /affiliate-payouts/:id`; Affiliate JWT: `GET /affiliates/me/dashboard`, `GET /affiliates/me/commissions`, `POST /affiliates/me/payout-request`. `?ref=CODE` on checkout links the order (see `09-growth-features-build-spec.md`) |
| **Metafields (28)** | Merchant: `GET/PUT/DELETE /products/:id/metafields[/:key]`; metafields embedded in public `GET /products/:id` |
| **Bulk CSV (29)** | Merchant: `POST /products/import` (multipart CSV → job id), `GET /products/import/:jobId` (status + per-line report), `GET /products/export` (sync CSV of all products) |
| **Feeds (30)** | Public w/ X-Tenant-ID: `GET /feeds/google-shopping.xml`, `GET /feeds/meta-catalog.csv` — active products only, storefront = custom_domain else `https://{subdomain}.{appBaseDomain}` |

Full contract in `shopkeet-agents-package (1)/docs/api-reference.md`.

---

## 6. Issues found & fixed (the history future agents should not re-discover)

1. **RLS was inert in prod (superuser bypass)** — API ran as `shopkeet` (superuser) until
   2026-09-28, so `FORCE RLS` was skipped and `tenant_isolation` policies did nothing.
   Fixed by switching prod `DATABASE_URL` to `shopkeet_app`. **Convention that emerged:**
   every non-HTTP path must `SET LOCAL app.current_tenant` itself (cart-recovery sweep,
   notification senders) or it will silently leak across tenants under RLS.
2. **pgx v5 scan traps — surfaced live on Phase 24** (integration tests couldn't run
   locally — DB down — so these only showed up in prod smoke):
   - Postgres `date` → Go `string` scan **fails**; scan into `time.Time`, format later.
   - `SUM()`/`COUNT()` return `bigint`; to scan into Go `int` cast `::int` in SQL.
   - `ORDER BY <aggregate>` binds to the ungrouped *input* column unless the aggregate in the
     SELECT has an **output alias** (`AS quantity`, `AS revenue_cents`).
3. **Status transitions are strict:** `pending→confirmed→shipped→delivered`;
   `pending→shipped` = `400 "invalid status transition"`; `cancelled` admitted only from
   `pending`/`confirmed`. `delivered` sets `payment_status='paid'` and fires `order.paid`.
4. **PowerShell/ops traps learned:** hand-copied JWTs get corrupted (401 "invalid token") —
   mint programmatically via `signtoken.exe`; `+` in phone-query params decodes as space —
   URL-encode; nested SQL over ssh breaks — write a `.sql`, `scp` it, `docker cp` it,
   `psql -f`; `.sh` scripts get CR chars — `tr -d '\r'` (GNU `sed 's/\r$//'` does **not**
   interpret `\r`); Coolify deploy can briefly race old/ex-new containers.
5. **Invoice PDF (P23):** generated with `go-pdf/fpdf v0.9` core fonts (Latin-1 only) —
   non-Latin-1 runes render as `?` (e.g. `Café 🐝` → `Café ?`); currency symbols handled for
   `usd/eur/gbp/pkr`, ISO code otherwise. Pure-math unit test proves totals can never
   disagree with `orders.total_cents`. **Accepted product decision (2026-10-02):** merchants
   operate English-only; no Urdu/non-Latin-1 support planned — this caveat is permanent, not a
   TODO. Don't re-open without an explicit product ask.
6. **`order_items.order_id` is NOT auto-indexed post-FK** — the §24 spec claimed it was;
   Postgres only indexes the referenced side. `0026` adds the explicit index.
7. **Gift-card double spend** — prevented by idempotency keys + `FOR UPDATE`
   (`TestGiftCardConcurrentDoubleSpend`). Loyalty "one credit per order" enforced by partial
   unique indexes.

---

## 7. Known issues / caveats (open, deliberately NOT fixed — read before you build on them)

1. **Analytics conversion undercounts carts (the "cart events" issue).** Checkout
   **deletes** the cart row, so `/analytics/conversion` `carts_created` counts only carts
   **still in the table** (i.e. abandoned ones). In live smoke: 4 carts were created but the
   endpoint reported `carts_created=1`. `conversion_rate = orders_placed / carts_created`
   is therefore inflated and the number is *accurate-but-mislabeled* — not a true
   "cart events" funnel. Documented data-model constraint. **To fix in a future phase**:
   keep carts with a `status` and mark `placed` instead of deleting, or add a
   `cart_events` table (created/checkout events).
2. **Single-currency assumption.** Analytics responses hardcode `currency:"usd"`; products,
   orders, gift cards, and discounts all carry a per-row currency, but a tenant is assumed
   to run one currency (`tenants.default_currency`). Cross-currency math not implemented.
3. **English-only full-text search.** `products.search_vector` uses
   `to_tsvector('english', …)`; fine for Urdu/PK stores only if they don't search in English
   (a PK store would need a per-tenant language config — future work).
4. **BOGO / product discounts are schema-reserved, not implemented.** `applies_to='product'`
   and any non-null `buy_quantity`/`get_quantity` → `400 "product (BOGO) discounts are not
   supported yet"`.
5. **Template `scope` and `blog_archive`/`search_results` template types** are schema-ready
   but not built. Post/template **revision history** is explicitly deferred.
6. **Wishlist is customer-JWT only** — no guest wishlists (deliberate).
7. **Payments: Cash-on-Delivery only** for v1. No gateway. `payment_status` flips to `paid`
   only on `delivered`.
8. **Notifications use Resend in prod**; SMTP/Mailpit is dev-only and must never be left
   configured for a real merchant.
9. **Local integration suite needs a running DB** (currently down on the dev box) — those
   tests skip locally; the authoritative acceptance proof is the VPS/live smoke.

---

## 8. Live verification (what was actually proven against prod)

- **Phases 0–21:** signup/login tenant flows, RLS cross-tenant probes (tenant B saw 0 of
  tenant A's data via psql while superuser saw all), catalog + variant + zone/rate +
  discount + gift card + guest cart (2×2000 → 400 discount → 1000 gift card) → checkout
  `201`, admin orders/cards/discounts, media, settings, customer signup/login, review
  create/list. Cleanup after each smoke — all throwaway `p2x-smoke` tenants removed, real
  data untouched (506 tenants / 135 orders at P18).
- **Phase 23 (8/8 PASS):** guest checkout `201` (total 2500) → `PATCH status` to
  `shipped` with tracking → customer lookup echoes tracking → wrong phone `404` →
  → `delivered` preserves tracking → admin invoice PDF `200 application/pdf` (`%PDF-`) →
  customer invoice PDF `200` → wrong-phone PDF `404`.
- **Phase 24 (all PASS):** seeded tenant + admin-created second product; 3 guest checkouts
  (A×3: 6500, B×2: 10500, A×1 then cancelled: 2500) + 1 abandoned cart. `?period=30d`
  results reconciled **exactly** against hand-run psql:
  - `/analytics/sales` → totals `23500` / `3` (cancelled 2500 excluded), bucket `2026-10-01`
    `23500`/`3` ✓
  - `/analytics/top-products?metric=quantity` → A qty 6 (r12000), B qty 2 (r10000) ✓
  - `/analytics/top-products?metric=revenue` → ranking matches revenue (r12000 ≥ r10000) ✓
  - `/analytics/conversion` → carts 1 / orders 4 / rate 4 ✓ (seed script itself places an
    order and checkout deletes carts — see caveat #1)
  - bad `period` (`45d`) → `400`, bad `metric` (`units`) → `400` ✓
- **Phase 27 (PASS as `shopkeet_app`):** `TestAffiliateProgram` — apply → duplicate `409` →
  merchant approve 10% → pending-login `403` → approved-login → bogus ref ignored → valid
  `?ref=` checkout books a 200¢ pending commission (10% of 2000¢ subtotal) → `delivered`
  approves it → payout-request `201` (200¢) / duplicate `409` → merchant-paid flips comm to
  `paid` → dashboard totals reconcile → affiliate JWT `403` on merchant-admin and customer
  routes → tenant B cannot touch tenant A's payout (`404`). Ran **as `shopkeet_app`** — the
  superuser bypasses RLS (that's how the earlier false positive happened); production role is
  the one that genuinely gates. 6 seeded `aff-*` tenants purged leaf-first, orphan sweep = 0.
- Deploy pipeline verified repeatedly: build → `scp` `migrate-linux` → apply migration →
`p21-deploy.ps1` → healthz 200 → smoke → cleanup.
- **Phases 28–30 (PASS as `shopkeet_app` on DB 31):**
  - `TestProductMetafields` (P28) — seeded `mf28-alpha/mf28-beta`; admin `PUT /products/:id/metafields`
    (key=size), public `GET /products/:id` embeds `metafields` `[{key,value,type}]`, admin `GET`
    list, cross-tenant read `404`, bad type `400`, missing key `404`; also `404` (not `500`) for a
    non-UUID product id (`does-not-exist`) — fixed via `uuid.Parse` guard on `productExists`.
  - `TestProductCSVImportExport` (P29) — 4-row import: alpha/beta/gamma inserted, `badstatus` row
    → `report errors [{Line:4 Error:"invalid status \"badstatus\" (draft|active|archived)"}]`;
    duplicate-name batch dedupes slugs (`dup-thing`/`dup-thing-2`); `GET /products/export` returns
    headered CSV for all products (first-name test builds the CSV from the import row).
  - `TestProductFeeds` (P30) — active+in-stock product → `<item>` with `g:id/price` as
    `<g:price>45.00 USD</g:price>` + `<g:availability>in_stock</g:availability>`; active but
    out-of-stock → `out_of_stock`; draft product omitted; `link` uses custom_domain /
    `shopbase.test` appBase; Meta CSV `id,title,description,link,image_link,availability,price,condition`
    with `45.00_USD`; missing `X-Tenant-ID` → `400`.
  - Migration `0031` applied (`schema_migrations = 31 | f`); RLS `ENABLE + FORCE` on
    `product_metafields` as `shopkeet_app` w/ `tenant_isolation` ALL policy (NULLIF wrap).
    Seed tenants purged leaf-first, orphan sweep = 0.

---

## 9. Ops playbook (for the agent that continues this project)

```bash
# Deploy latest main to prod
ssh -i C:\Users\Usama\.ssh\shopkeet_key_pair.pem -o StrictHostKeyChecking=no ubuntu@13.61.125.59
bash /tmp/...                                     # any VPS-side prep
powershell -File C:\Users\Usama\AppData\Local\Temp\opencode\p21-deploy.ps1   # Coolify deploy (local)
# watch for deployment_uuid=… final_deployment_status=finished healthz=200

# Apply an embedded migration (linux binary, rebuilt locally like migrate-linux)
scp -i <key> migrate-linux ubuntu@13.61.125.59:/tmp/migrate-linux
ssh … 'DATABASE_URL=postgres://shopkeet_app:shopkeet_app@10.0.1.10:5432/shopkeet?sslmode=disable /tmp/migrate-linux'
# verify: psql "SELECT version, dirty FROM schema_migrations ORDER BY version DESC LIMIT 1;"

# SQL against prod (avoid nested-quote hell): write file → scp → docker cp → psql -f
ssh … 'sudo docker cp /tmp/q.sql shopkeet-postgres:/tmp/q.sql; sudo docker exec shopkeet-postgres psql -U shopkeet -d shopkeet -f /tmp/q.sql'

# Smoke a tenant: bash /tmp/p21-seed.sh (echoes tid/pid/vid/zone/rate) → mint admin JWT:
#   $env:JWT_SECRET = "<prod-secret>"; $tok = (& C:\Users\Usama\AppData\Local\Temp\opencode\signtoken.exe admin <tid>).Trim()
# Cleanup: sed-in the TID into a *-cleanup.sh, tr -d '\r', run, assert p2x-smoke count = 0.
```

**Non-negotiables on this repo:** never edit an applied migration; every new tenant-scoped
table ships RLS+FORCE+policy+`OWNER TO shopkeet_app` in the same migration; update
`api-reference.md`/`schema.sql` in the same change that changes an API shape/schema; don't
start the next phase until the current one has a passing automated acceptance test; never
stage/commit the `shopkeet-agents-package*/` directories or `*.zip`.

---

## 10. Git state (as of Phase 30)

`main` = latest shipped code incl. Phases 28–30 (DB 31). Recent commits:
`4bc6780 feat(metafields,bulkcsv,feeds): Phases 28-30 metafields, bulk CSV import/export, product feeds (DB 31)` ·
`f0edbdc feat(affiliates): Phase 27 affiliate program (DB 30)` ·
`a0b89bb fix(rls): NULLIF-wrap tenant_isolation policies + fix Phase 26 acceptance tests (DB 29)` ·
`a02af9f docs: Phase 26 upsell, cross-sell and post-purchase recommendations (DB 28)`

## 11. Not built yet (deferred — when you get here, check `04-agent-build-spec.md`)

- `/webhooks/*` (developer marketplace) and a public/versioned developer API (GraphQL).
- Online payment beyond COD.
- Revision history (posts/templates).
- `blog_archive`, `search_results`, `announcement_bar` template types (schema-ready).
- BOGO / product-scoped discounts; per-category template scopes.
- True cart-events funnel (see caveat #1).