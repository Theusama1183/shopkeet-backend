# Shopkeet — All-Phases Report (Phases 1–33, DB 33)

**Date:** 2026-10-04
**Deployment:** live on `https://api.shopkeet.com` (Coolify-managed, healthz 200) — deploy is a manual Coolify trigger (`p21-deploy.ps1`), **not** automatic on git push. Despite `settings.is_auto_deploy_enabled=true` on the Coolify app, no Git source/webhook is connected (`source_id=0`, `webhook_token_url=null`, verified 2026-10-04 via `GET /api/v1/applications/l6modsyezs1vlrv6ly1oqz4i`), so pushes to `main` never fire a build; every release is deployed manually. Docs previously claiming git-push auto-deploy were corrected to match this reality (`AGENTS.md`, `docs/02-tech-stack.md`, `docs/03-architecture.md`, `.agents/rules/backend-constraints.md`, `SHOPKEET-COOLIFY-MIGRATION.md`).
**DB:** PostgreSQL 16, `shopkeet-postgres` manual container, `schema_migrations = 33 | dirty=f`, 44 base tables.
**Stack:** Go monolith (Fiber + pgx v5 + asynq on Redis 7) · multi-tenant, RLS `ENABLE+FORCE` + per-table `tenant_isolation` policy + `OWNER TO shopkeet_app`.
**Purpose of this report:** a single self-contained document a reviewer can hold against the code. Each phase lists what it built, its migration, the tables/routes it added, and the acceptance/verification evidence. Everything here was verified against live prod (not just unit-tested).

---

## 0. Global security & data contract (applies to every phase)

- **Two DB roles:** `shopkeet` (superuser — migrations/admin only), `shopkeet_app` (non-superuser, owns every tenant table; this is what the API connects as in prod). All acceptance tests run as `shopkeet_app`.
- **RLS contract (every tenant-scoped table, in the same migration):**
  `tenant_id uuid NOT NULL REFERENCES tenants(id)` + `ENABLE ROW LEVEL SECURITY` + `FORCE ROW LEVEL SECURITY` + `POLICY tenant_isolation … USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)` + `OWNER TO shopkeet_app`.
  The `NULLIF` wrap (migration 0029) makes an out-of-transaction query fail *closed* (0 rows) instead of raising `22P02`.
- **Non-HTTP paths** (scheduled jobs: cart-recovery sweep, notification senders, commerce auto-recompute) must `SET LOCAL app.current_tenant` themselves or RLS silently returns 0 rows.
- Original bug history: API *used to* run as superuser (`shopkeet`), which bypassed FORCE RLS → bullying. Prod switched to `shopkeet_app` on 2026-09-28. This is why every acceptance suite runs as `shopkeet_app`.

---

## 1. Phase-by-phase inventory

### Phase 1 — Auth (tenants, merchant_users, JWT scopes)
- **Migration:** 0002 · **Tables:** tenants, merchant_users.
- **Endpoints:** `POST /auth/signup` (tenant + owner atomically), `POST /auth/login`.
- **JWT:** HS256, claims carry `tenant_id` anchor + exactly one of `user_id+role` (merchant), `customer_id` (customer), `affiliate_id` (affiliate) `scope`. Middleware feeds `tenant_id` → `app.current_tenant` so RLS scopes every query.
- **Test:** `TestTenantRLSIsolation` · **Evidence:** live signup/login, cross-tenant psql probes (tenant B saw 0 of A, superuser saw all).

### Phase 2 — Media / Cloudflare R2
- **Migration:** 0005 · **Table:** media_assets.
- **Endpoints:** `POST /media/upload-url` (presigned), `POST /media`, `GET /media`, `DELETE /media/:id`.
- **Test:** `TestMediaRLSIsolation`.

### Phase 3 — Catalog
- **Migration:** 0006 · **Tables:** categories, products, product_categories, product_images.
- **Endpoints:** public `GET /products`, admin CRUD products/categories/images; generated `products.search_vector` GIN (English `to_tsvector`).
- **Test:** `TestCatalogRLSIsolation`.

### Phase 4 — Cart & Inventory (Redis reservation)
- **Migration:** 0007 · **Tables:** carts, cart_items.
- **Endpoints:** `GET/POST /cart`, `PATCH/DELETE /cart/items/:id`.
- **Test:** `TestCartRLSIsolation`, `TestRedisReserver` (skips without Redis). Redis reserve → at checkout the reservation is enforced against `FOR UPDATE` rows.

### Phase 5 — Checkout & Orders (COD)
- **Migration:** 0008 · **Tables:** orders, order_items.
- **Endpoints:** `POST /checkout`, `GET /orders/:id` (customer lookup), `GET /orders` (admin).
- **Status flow is strict:** `pending→confirmed→shipped→delivered`; `cancelled` from pending/confirmed only; `delivered` → `payment_status=paid` + fires `order.paid` event.
- **Test:** `TestOrdersRLSIsolation`.

### Phase 6 — Content & Page Builder
- **Migration:** 0009 · **Tables:** posts, templates, sections, redirects.
- **Endpoints:** CRUD `/posts`, `/templates/:template_type`, `/sections`, `/redirects`, `/redirects/lookup?path=`. Layouts are Puck JSONB.
- **Test:** `TestContentRLSIsolation`.

### Phase 7 — Observability
- **Migration:** none · **Endpoints:** `GET /healthz`, `GET /metrics`; unified error shape.
- **Test:** `TestErrorShape`, `TestMetricsExposition`.

### Phase 8 — Product Variants
- **Migration:** 0010 · **Tables:** product_options, product_option_values, product_variants, product_variant_option_values.
- **Endpoints:** admin `/products/:id/variants`, public `/products/:id`.
- **Test:** `TestVariantsRLSIsolation`.

### Phase 9 — Shipping
- **Migration:** 0011 · **Tables:** shipping_zones, shipping_rates; orders gain the full address block + `shipping_method` + `shipping_cost_cents`.
- **Endpoints:** `/shipping-zones`, `/shipping-rates` + public rates fetch at checkout.
- **Test:** `TestShippingRLSIsolation`.

### Phase 10 — Discounts (manual codes)
- **Migration:** 0012 · **discounts**: flat/percent, `min_subtotal_cents`, `usage_limit`/`times_used`, `status`, per-code `starts_at/ends_at`.
- **Endpoints:** `POST/GET/PATCH /discounts`, `/discounts/:id`, `POST /cart/discount`.
- **Test:** `TestDiscountsAcceptance` (still green today — regressed on P33 work).

### Phase 11 — Customer Accounts
- **Migration:** 0013 · **Tables:** customers, customer_addresses; orders gain `customer_id`.
- **Endpoints:** `/customers/signup|login|me`.
- **Test:** `TestCustomersRLSIsolation`.

### Phase 12 — Notifications
- **Migration:** 0014 · **Table:** notification_log. Prod sender = **Resend**; SMTP/Mailpit dev-only (must never be configured for a real merchant).

### Phase 13 — Store Settings, Order Notes & Tax
- **Migration:** 0015 · tenants gains settings (logo, default_currency, timezone, support, `tax_rate_percent`); orders gain `internal_note` + `tax_cents`.
- **Endpoints:** `GET/PATCH /tenant/settings`.

### Phase 14 — Platform hardening
- **Migration:** 0016 · **Table:** idempotency_keys (+ Redis cache async).
- **Endpoints:** `GET /healthz`, `GET /metrics`, rate limiting, idempotency replay protection.
- **Test:** `TestIdempotencyReplay`, `TestRateLimitWindow`, cache tests.

### Phase 15 — Merchant Ops: Draft Orders & Returns
- **Migration:** 0017 · **Tables:** returns, return_items; orders gain `source` (`storefront|draft`).
- **Endpoints:** `POST /orders` (draft, admin, decrements stock), `POST /orders/:id/returns` (re-stock).
- **Test:** `TestDraftOrderDecrementsStock`, `TestReturnRestocksCorrectVariant`.

### Phase 16 — Product Reviews
- **Migration:** 0018 · **Table:** product_reviews; products gain `rating_average`, `rating_count` (recomputed on reject/delete). Verified = linked paid order.
- **Endpoints:** `POST /products/:id/reviews`, admin reject/delete.
- **Test:** `TestReviewLifecycleAndVerified`, `TestRejectAndDeleteRecompute`.

### Phase 17 — Abandoned Cart Recovery + lifecycle emails
- **Migration:** 0019 · carts gain `customer_email`, `last_activity_at`, `recovery_sent_at`; partial index for the sweep.
- **Endpoints:** `POST /cart/email`. **Sweep** is a scheduled job that must set its own tenant GUC (RLS).
- **Test:** `TestCartRecoverySweep`, `TestSendCartAbandoned`.

### Phase 18 — Gift Cards
- **Migration:** 0020 · **Table:** gift_cards; orders gain `gift_card_code` + `gift_card_cents`; carts gain `gift_card_code`.
- **Endpoints:** admin cards CRUD, `POST /cart/gift-card`, checkout snapshot.
- **Double-spend guard:** idempotency keys + `FOR UPDATE` (`TestGiftCardConcurrentDoubleSpend`).
- **Test:** `TestGiftCardsAdmin`, `TestGiftCardCheckoutSnapshot`, `TestGiftCardConcurrentDoubleSpend`.

### Phase 19 — Pre-orders & Back-in-Stock Alerts
- **Migration:** 0021 · product_variants gain `allow_preorder`, `preorder_ships_at`; order_items gain `is_preorder`; new `back_in_stock_subscriptions` (UNIQUE tenant variant + lower(email)); notification_log gains `back_in_stock` type.
- **Endpoints:** public `/products/:id/variants/:variantId/notify-me`.
- **Test:** `TestPreorderCheckout`, `TestNotifyMe`, `TestDeliverBackInStockExactlyOnce`.

### Phase 20 — Loyalty & Referrals
- **Migration:** 0022 · **Table:** loyalty_ledger (partial unique indexes `order_placed_once`, `referral_once` — one credit per order); tenants + customers gain loyalty config/points.
- **Endpoints:** `/customers/me/loyalty`, `/customers/me/referral`, `POST /loyalty/redeem`.
- **Test:** `TestLoyaltyReferrals`.

### Phase 21 — Advanced & Automatic Discounts
- **Migration:** 0023 · discounts gain `requires_code` (false = auto-promo on checkout), `applies_to` (`order|shipping`), reserved `buy_quantity`/`get_quantity` (BOGO → 400 "not supported yet"); P20 added `customer_id` (per-customer codes).
- **AutoPick:** best of auto vs. applied code wins at checkout.
- **Test:** `TestAutomaticDiscounts`.
- **Latent bug found & fixed on P33:** `'ap-'+`/`'bp-'+` SQL string concatenation was never concatenating (turned into `'bp-' ||` style).

### Phase 22 — Wishlist
- **Migration:** 0024 · **Table:** wishlist_items (UNIQUE customer+product; 409 on duplicate add). Customer-JWT only; no guest wishlists (deliberate).
- **Endpoints:** `/customers/me/wishlist`.
- **Test:** `TestWishlistFlow`.

### Phase 23 — Order Tracking & Invoice PDF
- **Migration:** 0025 · orders gain `tracking_number/carrier/url`.
- **Endpoints:** `PATCH /orders/:id/status` (advance + tracking), `GET /orders/:id/invoice.pdf` (admin **or** customer `?phone=`).
- **PDF:** go-pdf/fpdf core fonts (Latin-1 only — non-Latin-1 renders `?`; accepted product decision, English-only stores). Pure-math test proves PDF totals == `orders.total_cents`.
- **Test:** `TestOrderTrackingAndInvoicePDF`, `TestInvoiceTotalsMath` · **Live evidence:** 8/8 PASS incl. `%PDF-` bytes + wrong-phone 404.
- **Test bug found/fixed on P33:** raw `+` in the phone query param decodes to a space → lookup 404; test now `url.QueryEscape`s the phone (`net/url`).

### Phase 24 — Storefront Analytics
- **Migration:** 0026 · explicit analytics indexes.
- **Endpoints:** `GET /analytics/sales|top-products|conversion?period=7d|30d|90d` (Merchant).
- **Test:** `TestAnalyticsReconciliation`, `TestParsePeriodAndLimit` · **Live evidence:** sales/top-products/conversion reconciled exactly against hand-psql.
- **pgx traps (found live):** `date`→`string` scan fails (scan `time.Time`); `SUM/COUNT` bigint (cast `::int`); `ORDER BY <aggregate>` needs an output alias.
- **Open caveat:** conversion undercounts carts (checkout deletes the cart row; abandoned carts only are counted) — see §3.

### Phase 25 — Product Bundles & Quantity Breaks
- **Migration:** 0027 · bundles (fixed-flat / mix-and-match % off) + quantity breaks.
- **Test:** `TestBundlesAcceptance`.

### Phase 26 — Upsell / Cross-sell Recommendations + Post-purchase add-item
- **Migration:** 0028 · **Table:** product_recommendations (`type` CHECK `manual|auto`; UNIQUE(product_id, recommended_product_id, type) — deliberately tenant-less; `manual` reserved as merchant-curated in P26, `auto` reserved for the P32 job). Manages soft-deleted products (FKs to products, no cascade).
- **Endpoints:** public/admin `GET /products/:id/recommendations`, admin `POST`/`DELETE` (manual curation, idempotency-guarded POST).
- **Test:** `TestRecommendationsAcceptance` (still green today).

### Phase 27 — Affiliate Program
- **Migration:** 0030 · **Tables:** affiliates, affiliate_commissions, affiliate_payouts.
- **Endpoints:** public `POST /affiliates/apply`, `/affiliates/login`; merchant approve; affiliate JWT dashboard/commissions/payout-request; `?ref=CODE` on checkout books a commission (snapshot rules; `delivered` event flips pending→approved; payout `paid` covers all approved).
- **Test:** `TestAffiliateProgram` · **Evidence:** full lifecycle PASS as `shopkeet_app`.
- **Doc note:** migration numbering skips 0029 (0029 = RLS NULLIF wrap applied retroactively to older tables).

### Phase 28 — Metafields / Custom Fields
- **Migration:** 0031 · **Table:** product_metafields (UNIQUE product+key; type string|number|boolean|json).
- **Endpoints:** merchant `GET/PUT/DELETE /products/:id/metafields[/:key]`; metafields embedded in public product GET.
- **Test:** `TestProductMetafields` · **Bug fixed:** `uuid.Parse` guard so a non-UUID product id 404s (not 500).

### Phase 29 — Bulk CSV Import / Export
- **Migration:** none · Import via async asynq job + per-line report; export = synchronous CSV.
- **Endpoints:** `POST /products/import` (multipart → job id), `GET /products/import/:jobId`, `GET /products/export`.
- **Test:** `TestProductCSVImportExport` (per-row failure report, slug dedupe, export round-trip).

### Phase 30 — Product Feeds
- **Migration:** none · **Endpoints:** `GET /feeds/google-shopping.xml`, `GET /feeds/meta-catalog.csv` (public + X-Tenant-ID; active products only; storefront link = custom_domain else `https://{subdomain}.{appBaseDomain}`).
- **Test:** `TestProductFeeds` (g:price `45.00 USD`, availability, out-of-stock nuance, missing header → 400).

### Phase 31 — Smart (Rule-Based) Collections
- **Migration:** 0032 · **Tables:** smart_collection_rules (+ `categories.is_smart`). Rules AND-combined over `price|inventory_count|status|name|description`; empty rules = matches everything. Membership in `product_categories` recomputed on every product save (incl. bulk import) — storefront list path stays query-time-free.
- **Endpoints:** merchant `POST /categories` (inline middleware — Fiber v2 group middleware would leak `TenantMW` onto public GET), `PATCH/DELETE /categories/:id`; public GET exposes `is_smart`+`rules`.
- **Test:** `TestSmartCollections` · **Bugs hit/fixed:** stdlib `errors.As` for pgconn un-wrapping (23505); `description` nullable → `*string` scan; Fiber group middleware merge footgun.

### Phase 32 — Data-Driven Recommendations (auto)
- **Migration:** none (reuses `0028` schema, `type='auto'`).
- **Job:** `recommendations:auto` asynq task scheduled `@weekly` in `cmd/api/main.go`; `RecomputeAllTenants` iterates every tenant in its own RLS-scoped tx; `RecomputeAuto` deletes the tenant's `auto` rows then upserts top-5 co-occurring pairs (`autoMinOrders=2` shared orders, status not cancelled, both products active, never a pair that has a manual row). Manual curation wins and sorts first on the public/admin list (`manualPriority`).
- **Test:** `TestDataDrivenRecommendations` (seeds 2 products + 3 co-purchase orders, recomputes, asserts auto rows) + `TestRecommendationsAcceptance` regression.
- **Bug found by live smoke, fixed same day (commit `0410581`):** the recompute SQL was **not tenant-scoped** — `pairs` joined `order_items` across the whole DB, so an RLS-bypassed (superuser) run generated rows pairing *another tenant's* products into your tenant and collided on the tenant-less UNIQUE (aborted 0 rows). Harmless in prod (worker sets GUC → RLS scopes the job) but wrong SQL. Now `pairs`/`eligible` filter `a/b/o.tenant_id` and the products `EXISTS` pin the same tenant.

### Phase 33 — Customer Tags & Segments (tag-gated discounts)
- **Migration:** 0033 · **Tables:** customer_tags (UNIQUE tenant+customer+tag; `customer_tags_tag_idx (tenant_id, tag, created_at)`); discounts gain `eligible_tag` (nullable TEXT). RLS NULLIF policy + `OWNER TO shopkeet_app`.
- **Endpoints:** merchant `GET /customers?tag=vip` (segment), `POST /customers/:id/tags` `{"tag":"vip"}` (idempotent; normalizes lowercase; rejects multi-word/empty), `DELETE /customers/:id/tags?tag=vip`; tags embed on customer detail/list.
- **Gate semantics:** `eligible_tag` enforced at **cart apply** (guest → 400 "requires the vip tag") AND **re-verified inside the checkout transaction** (authoritative — remove the tag after apply, checkout still 400). Tag-gated automatic promos (`requires_code=false`) are skipped by `AutoPick` for untagged shoppers. Cart/checkout now run `CustomerOrGuestMW` so a signed-in customer's identity reaches the preview and checkout resolvers (`customerID(c)` helper).
- **Test:** `TestCustomerTagsAndSegments`.
- **Bugs hit/fixed live:** (1) cart preview resolved discounts with nil customer → tag-gated code previewed 0¢ — `loadCart` now passes the customer; (2) pgx `conn busy` — `AutoPick` queried `customer_tags` while its `FOR UPDATE` candidate cursor was open on the same connection; now drains + closes rows first; (3) `array_agg` over LEFT JOIN yields `{NULL}` → pgx can't scan into `[]string` — wrapped `COALESCE(… FILTER (WHERE … IS NOT NULL), '{}')`.
- **Live HTTPS smoke:** 25/25 PASS (P32: auto recompute → 2 rows, manual-before-auto on admin+public, no status leak on public; P33: segment isolation, guest 400, tagged cart VIPX/200 → checkout 2300, apply-then-untag → 400, auto promo 2500 untagged / 2400 tagged).

---

## 2. Complete schema at a glance (44 tables, DB 33)

RLS-forced, `tenant_id`-scoped, NULLIF `tenant_isolation` + `OWNER shopkeet_app` (except `tenants`, `schema_migrations` which are RLS-free):

`tenants` · `merchant_users` · `media_assets` · `categories` (+`is_smart`) · `products` · `product_metafields` · `product_categories` · `smart_collection_rules` · `product_images` · `carts` · `cart_items` · `orders` · `order_items` · `posts` · `templates` · `sections` · `redirects` · `product_options` · `product_option_values` · `product_variants` · `product_variant_option_values` · `shipping_zones` · `shipping_rates` · `discounts` · `customer_tags` · `product_recommendations` · `customers` · `customer_addresses` · `notification_log` · `idempotency_keys` · `returns` · `return_items` · `product_reviews` · `gift_cards` · `back_in_stock_subscriptions` · `loyalty_ledger` · `wishlist_items` · `affiliates` · `affiliate_commissions` · `affiliate_payouts` · `schema_migrations`

**Key non-PK indexes:** `products_search_idx` GIN (`search_vector`) · `carts_recovery_scan_idx` partial · loyalty partial uniques (`order_placed_once`, `referral_once`) · `orders_created_at_idx`, `order_items_order_idx`, `carts_created_at_idx` · `customer_tags_tag_idx` · `product_recommendations (product_id, type, sort_order)`.

---

## 3. Open caveats / deliberately not fixed (read before building on them)

1. **Analytics conversion undercounts carts** — checkout deletes the cart row, so `carts_created` counts only abandoned carts; `conversion_rate` is inflated-but-accurately-labeled. Future fix: keep carts with a placed status, or a `cart_events` table.
2. **Single-currency assumption** — analytics hardcodes `currency:"usd"`; tenant assumed to run one currency. Cross-currency math not implemented.
3. **English-only full-text search** (`to_tsvector('english', …)`).
4. **BOGO / product-scoped discounts schema-reserved only** (→ 400).
5. **Payments = COD only** for v1 (no gateway; `paid` flips on `delivered`).
6. **Template `blog_archive`/`search_results`/`announcement_bar` + revision history** deferred.
7. **Wishlist is customer-JWT only** (no guest; deliberate).
8. **Notifications run Resend in prod**; SMTP/Mailpit never for real merchants.
9. **Local integration tests need a running DB** (down on dev box) — authority is VPS/live smoke.

## 4. Deploy / verify state

- Commits on `main`: `9f0c457 feat(recommendations,customertags): Phases 32-33 … (DB 33)` · `0410581 fix(recommendations): scope Phase 32 auto-recompute to the tenant's own order history (DB 33)` (both pushed, both deployed).
- All acceptance binaries run against prod as `shopkeet_app` with `-test.count=1`: `rec33-test-linux` (`TestDataDrivenRecommendations` + `TestRecommendationsAcceptance`), `tag33-test-linux` (`TestCustomerTagsAndSegments`), `regress-disc-linux`, `regress-orders-linux`, `regress-cart-linux` — **all green in a single pass**.
- Live HTTPS smoke `smoke-sc33.ps1`: **25/25 PASS**; 0 test/smoke tenants remain after purge.

## 5. RLS-bypass-window audit closure (2026-10-04)

The superuser-bypass window (before the 2026-09-28 switch of the live DB role to `shopkeet_app`) is closed on **both** axes it could have mattered:

1. **Tenants created before the fix** (`10-audit-remediation.md` Action 2): prior scan found all pre-fix tenants are test artifacts (450 `*-alpha/beta-*`, 30 `auth-*`, 24 `idem/tax/cache`, 2 other) — **zero real signups**, zero unauthorized data.
2. **Cross-tenant data pollution in business tables** (run live 2026-10-04 as `shopkeet` superuser, `integrity-scan.sql`): **0 violations** across 39 child→parent checks (order_items, cart_items, product_recommendations, product_variants/options, images, metafields, reviews, wishlist, back-in-stock, tags, addresses, loyalty, discounts, affiliates, returns, smart rules, shipping, categories, media, tenants.logo) comparing `tenant_id` between rows and their referenced parents, plus **0 orphan references** across 11 checks. No cross-tenant or orphaned rows exist in production data.