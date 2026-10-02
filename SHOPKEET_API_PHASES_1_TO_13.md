# Shopkeet API — Complete Reference (Phases 1–13)

**Last updated:** 2026-10-02  
**DB version:** 26 (migrations 0001–0026 applied on VPS)  
**Deployment:** live on `https://api.shopkeet.com` (Coolify-managed, healthy)  
**All acceptance tests:** PASS  
**Stack:** Go 1.27 · Fiber · pgx/pgxpool · PostgreSQL 16 (RLS + FORCE) · Redis 7 · golang-migrate (embedded)

---

## Table of Contents

1. [Auth (Phase 1)](#auth-phase-1)
2. [Media / Cloudflare R2 (Phase 2)](#media--cloudflare-r2-phase-2)
3. [Catalog (Phase 3)](#catalog-phase-3)
4. [Cart & Inventory (Phase 4)](#cart--inventory-phase-4)
5. [Checkout & Orders (Phase 5)](#checkout--orders-phase-5)
6. [Content & Page Builder (Phase 6)](#content--page-builder-phase-6)
7. [Observability (Phase 7)](#observability-phase-7)
8. [Product Variants (Phase 8)](#product-variants-phase-8)
9. [Shipping (Phase 9)](#shipping-phase-9)
10. [Discounts (Phase 10)](#discounts-phase-10)
11. [Customer Accounts (Phase 11)](#customer-accounts-phase-11)
12. [Notifications (Phase 12)](#notifications-phase-12)
13. [Store Settings, Order Notes & Tax (Phase 13)](#store-settings-order-notes--tax-phase-13)
14. [Merchant Operations: Draft Orders & Returns (Phase 15)](#merchant-operations-draft-orders--returns-phase-15)
15. [Product Reviews (Phase 16)](#product-reviews-phase-16)
16. [Abandoned Cart Recovery & Lifecycle Emails (Phase 17)](#abandoned-cart-recovery--lifecycle-emails-phase-17)
17. [Gift Cards (Phase 18)](#gift-cards-phase-18)
18. [Pre-orders & Back-in-Stock Alerts (Phase 19)](#phase-19--pre-orders--back-in-stock-alerts)
19. [Loyalty & Referrals (Phase 20)](#loyalty--referrals-phase-20)
20. [Advanced & Automatic Discounts (Phase 21)](#advanced--automatic-discounts-phase-21)
21. [Wishlist (Phase 22)](#wishlist-phase-22)
22. [Order Tracking & Invoice PDF (Phase 23)](#order-tracking--invoice-pdf-phase-23)
23. [Storefront Analytics (Phase 24)](#storefront-analytics-phase-24)
24. [Product Bundles & Quantity Breaks (Phase 25)](#product-bundles--quantity-breaks-phase-25)
25. [Upsell, Cross-sell & Post-Purchase Recommendations (Phase 26)](#upsell-cross-sell--post-purchase-recommendations-phase-26)
26. [Affiliate Program (Phase 27)](#affiliate-program-phase-27)
27. [Metafields / Custom Fields (Phase 28)](#metafields--custom-fields-phase-28)
28. [Bulk CSV Import/Export (Phase 29)](#bulk-csv-importexport-phase-29)
29. [Product Feeds (Phase 30)](#product-feeds-phase-30)
26. [Database Schema Summary](#database-schema-summary)
16. [Auth Scopes & Middleware](#auth-scopes--middleware)
17. [Error Shape](#error-shape)
18. [Env Vars & Config](#env-vars--config)

---

## Auth (Phase 1)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/auth/signup` | Public | Create tenant + owner user in one transaction. Body: `{name, subdomain, email, password}`. Returns `{token, expires, user, tenant}`. |
| POST | `/auth/login` | Public | Returns JWT `{tenant_id, user_id, role, scope:"merchant"}`. Body: `{email, password, subdomain}`. |

### JWT Claims (Merchant)

```json
{
  "tenant_id": "uuid",
  "user_id": "uuid",
  "role": "owner|staff",
  "scope": "merchant",
  "exp": 1234567890
}
```

### Tables

- `tenants` (id, name, subdomain, custom_domain, status, logo_media_asset_id, default_currency, timezone, support_email, support_phone, tax_rate_percent, created_at)
- `merchant_users` (id, tenant_id, email, password_hash, role, created_at)

---

## Media / Cloudflare R2 (Phase 2)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/media/upload-url` | Admin | Request presigned R2 PUT URL. Body: `{filename, content_type}`. Returns `{upload_url, r2_key, expires_in}`. |
| POST | `/media` | Admin | Confirm upload; records metadata. Body: `{r2_key, content_type, size_bytes, alt_text}`. Returns asset with public `url`. |
| GET | `/media` | Admin | List tenant's media library. |
| DELETE | `/media/:id` | Admin | Delete from R2 and DB. |

### Table

- `media_assets` (id, tenant_id, r2_key, content_type, size_bytes, alt_text, created_at)

### Env Vars (required together or not at all)

`R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_BUCKET_NAME`, `R2_PUBLIC_URL`

---

## Catalog (Phase 3)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/products` | Public | List `status=active` products. Query: `?search=&category=`. |
| GET | `/products/:id` | Public / Admin | Fetch one product with images, options, variants. Admin sees all statuses. |
| POST | `/products` | Admin | Create product; auto-creates one "Default" variant. Body: `{name, slug, price_cents, inventory_count, status?, category_ids?}`. |
| PATCH | `/products/:id` | Admin | Update product; cascades price/inventory to sole variant. |
| DELETE | `/products/:id` | Admin | Remove product (409 if variants referenced by carts/orders). |
| POST | `/products/:id/images` | Admin | Attach media asset. Body: `{media_asset_id, sort_order}`. |
| DELETE | `/products/:id/images/:imageId` | Admin | Remove image from product. |
| POST | `/products/:id/options` | Admin | Create option with values. Body: `{name, values[]}`. |
| POST | `/products/:id/variants` | Admin | Create variant. Body: `{option_value_ids[], sku?, price_cents, inventory_count, weight_grams?, status?}`. |
| PATCH | `/products/:id/variants/:variantId` | Admin | Update variant; `option_value_ids` replaces links when provided. |
| DELETE | `/products/:id/variants/:variantId` | Admin | Delete variant (400 if last variant; 409 if referenced). |
| GET | `/categories` | Public | List categories. |

### Product JSON (detail)

```json
{
  "id": "...",
  "name": "...",
  "slug": "...",
  "price_cents": 1999,
  "inventory_count": 50,
  "currency": "usd",
  "status": "active",
  "images": [{"id": "...", "url": "...", "alt_text": "..."}],
  "categories": [{"id": "...", "name": "..."}],
  "options": [
    {"id": "...", "name": "Size", "values": [{"id": "...", "value": "S"}, {"id": "...", "value": "M"}]}
  ],
  "variants": [
    {"id": "...", "option_values": [{"option_id": "...", "value_id": "..."}], "sku": "ABC-123", "price_cents": 1999, "inventory_count": 10, "weight_grams": 200, "status": "active"}
  ]
}
```

### Product Detail Cache-aside (Redis) — Phase 14

Public storefront reads of `GET /products/:id` are served from a cache-aside layer (1 min TTL). Admin reads bypass the cache (always fresh); admin writes invalidate the entry for both viewers.

- Keys: `shopkeet:cache:product:{tenant_id}/{product_id}/{viewer}` where viewer ∈ `public|admin`. Entries are never shared between viewers.
- Read flow (viewer=`public`): cache GET → hit sends raw JSON; miss runs the Postgres detail query and populates the cache before responding.
- Invalidation on any write that can change the serialized product: `PATCH /products/:id`, `DELETE /products/:id`, `POST|DELETE /products/:id/images`, option/variant create/update/delete (incl. the sole-variant cascade), and the successful checkout aggregate-refresh in `orders`.
- Fallback: when `REDIS_URL` is unset the app uses an in-memory `Noop` cache — reads always hit Postgres and everything stays correct.
- Redis also powers cart reservation (`shopkeet:reserve:{variant_id}:{unit_index}`, TTL 15 min) and per-route rate limiting (`shopkeet:rl:{route}:{linger}:{key}`).

### Tables

- `categories`, `products`, `product_categories`, `product_images`
- `product_options`, `product_option_values`, `product_variants`, `product_variant_option_values`

---

## Cart & Inventory (Phase 4)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/cart` | Customer | Fetch current cart by session (cookie `shopkeet_session` or header `X-Customer-Session`). Returns `null` if empty. |
| POST | `/cart` | Customer | Create/add item. Body: `{variant_id, quantity}`. Same variant merges quantity. |
| PATCH | `/cart/items/:id` | Customer | Change quantity. Body: `{quantity}`. |
| DELETE | `/cart/items/:id` | Customer | Remove item. |

### Cart JSON

```json
{
  "id": "...",
  "customer_session": "...",
  "discount_code": "SAVE10",
  "discount_cents": 200,
  "items": [
    {"id": "...", "variant_id": "...", "quantity": 2, "unit_price_cents": 1999, "line_total_cents": 3998, "product": {...}, "variant": {...}}
  ],
  "subtotal_cents": 3998,
  "total_cents": 3798
}
```

### Reservation (Redis, best-effort)

Keys: `shopkeet:reserve:{variant_id}:{unit_index}` (TTL 15 min)  
Checkout uses `SELECT ... FOR UPDATE` as authoritative guard.

### Tables

- `carts` (id, tenant_id, customer_session, discount_code, created_at) — UNIQUE (tenant_id, customer_session)
- `cart_items` (id, tenant_id, cart_id, variant_id, quantity, created_at) — UNIQUE (cart_id, variant_id)

---

## Checkout & Orders (Phase 5)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/checkout` | Customer | Guest or customer JWT. Body: `{customer_name, customer_phone, customer_email?, shipping_address_line1, shipping_address_line2?, shipping_city, shipping_state?, shipping_postal_code?, shipping_country, shipping_rate_id, payment_method:"cod"}`. Returns created order. |
| GET | `/orders/:id` | Customer | Lookup by id + `?phone=` [&`email=`]. |
| GET | `/orders` | Admin | List tenant orders. Query: `?status=`. |
| PATCH | `/orders/:id/status` | Admin | Transition: `pending → confirmed → shipped → delivered` (sets `payment_status=paid`), `pending/confirmed → cancelled`. Skip-step → 400. |
| PATCH | `/orders/:id/note` | Admin | Set/clear internal note (merchant-only). Body: `{note: "..."}`. |

### Order JSON (admin — `includeInternalNote=true`)

```json
{
  "id": "...",
  "customer_id": "uuid|null",
  "customer_name": "Ada",
  "customer_phone": "+1-555-0001",
  "customer_email": "ada@example.com",
  "shipping_address_line1": "2 Main St",
  "shipping_address_line2": "",
  "shipping_city": "Lahore",
  "shipping_state": "PB",
  "shipping_postal_code": "54000",
  "shipping_country": "PK",
  "shipping_method": "Standard",
  "shipping_cost_cents": 500,
  "discount_code": "SAVE10",
  "discount_cents": 200,
  "tax_cents": 190,
  "internal_note": "VIP customer",
  "payment_method": "cod",
  "payment_status": "pending",
  "status": "pending",
  "total_cents": 2489,
  "currency": "usd",
  "created_at": "2026-09-25T12:00:00Z",
  "items": [{"id": "...", "product_id": "...", "variant_id": "...", "quantity": 1, "unit_price_cents": 1999, "line_total_cents": 1999}]
}
```

**Customer-facing order JSON excludes `internal_note`.**

### Total Formula (Phases 5 + 9 + 10 + 13)

```
tax_cents      = subtotal_cents * tenants.tax_rate_percent / 100
total_cents    = subtotal_cents - discount_cents + shipping_cost_cents + tax_cents
```

### Tables

- `orders` (id, tenant_id, customer_id, customer_name, customer_phone, customer_email, shipping_address [deprecated], shipping_address_line1/2, shipping_city, shipping_state, shipping_postal_code, shipping_country, shipping_method, shipping_cost_cents, discount_code, discount_cents, tax_cents, internal_note, payment_method, payment_status, status, total_cents, currency, created_at)
- `order_items` (id, tenant_id, order_id, product_id, variant_id, quantity, unit_price_cents)

### Events (internal bus)

- `order.created` — emitted after order INSERT
- `order.paid` — emitted when order reaches `delivered`

---

## Content & Page Builder (Phase 6)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/posts?post_type=page&route=/` | Public | Fetch published post by type+route. |
| GET | `/posts?post_type=blog_post` | Public | List published posts of type. |
| POST | `/posts` | Admin | Create post. Body: `{post_type, route, title, layout, meta_title?, meta_description?, og_image_id?, status?}`. |
| PATCH | `/posts/:id` | Admin | Update post. |
| DELETE | `/posts/:id` | Admin | Remove post. |
| GET | `/templates/:template_type` | Public | Fetch active template (scope=default). |
| PUT | `/templates/:template_type` | Admin | Create/update template (Puck save). |
| GET | `/sections?section_type=header` | Public | Fetch published header/footer. |
| GET | `/sections?section_type=popup` | Public | List active popups. |
| POST | `/sections` | Admin | Create section (mainly popups). |
| PATCH | `/sections/:id` | Admin | Update section layout/placement/status. |
| DELETE | `/sections/:id` | Admin | Remove section. |
| GET | `/redirects` | Admin | List redirects. |
| POST | `/redirects` | Admin | Create redirect (auto-created on post route rename). |
| DELETE | `/redirects/:id` | Admin | Remove redirect. |
| GET | `/redirects/lookup?path=/old-page` | Public | Next.js middleware uses this. |

### Tables

- `posts` (id, tenant_id, post_type, route, title, layout JSONB, meta_title, meta_description, og_image_id, status, published_at, updated_at) — UNIQUE (tenant_id, route)
- `templates` (id, tenant_id, template_type, layout JSONB, scope, status, updated_at) — UNIQUE (tenant_id, template_type, scope)
- `sections` (id, tenant_id, section_type, layout JSONB, placement_rules JSONB, status, updated_at)
- `redirects` (id, tenant_id, from_path, to_path, created_at) — UNIQUE (tenant_id, from_path)

### Signup Seed Hook

New tenant gets: home page at `/`, 5 templates (`product`, `product_archive`, `cart`, `404`, `order_confirmation`), `header`, `footer` — all `published`.

---

## Observability (Phase 7)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/healthz` | Public | `{status: "ok"}`. |
| GET | `/metrics` | Internal | Prometheus scrape. Bearer-gated when `METRICS_TOKEN` set. |

### Metrics

- `shopkeet_http_requests_total{method,route,status}`
- `shopkeet_http_request_duration_seconds{method,route}` (histogram)
- `shopkeet_db_pool_{max,acquired,idle,total}`

### Middleware Order

`Recover → RequestID (X-Request-ID) → AccessLog (JSON) → Metrics`

### Logging

Structured JSON per request: `request_id`, `method`, `path`, `status`, `latency_ms`, `tenant_id`, `role`.

---

## Product Variants (Phase 8)

### Key Changes

- Every product **always has ≥1 variant** (auto "Default" variant on create).
- Product price = MIN(active variant price); product stock = SUM(active variant stock) — cached on `products` and recomputed on variant changes.
- Cart/orders reference `variant_id` (not `product_id` directly).
- `cart_items` unique key: `(cart_id, variant_id)`.
- Reservation keys: `shopkeet:reserve:{variant_id}:{unit_index}`.

### Variant Admin Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/products/:id/options` | Admin | Create option + values. |
| POST | `/products/:id/variants` | Admin | Create variant with option value links. |
| PATCH | `/products/:id/variants/:variantId` | Admin | Update variant (sku, price, stock, weight, status, option values). |
| DELETE | `/products/:id/variants/:variantId` | Admin | Delete (400 if last variant; 409 if referenced). |

---

## Shipping (Phase 9)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/shipping/rates?country=&state=` | Public | All rates of zones covering destination. `free_over_cents` included. Response adds `state_required: true` when the country has region-restricted zones but no state was given (frontend should then require state). |
| POST | `/shipping/zones` | Admin | Create zone: `{name, countries[], regions[]}`. |
| PATCH | `/shipping/zones/:id` | Admin | Update zone. Type change with rates → 409. |
| DELETE | `/shipping/zones/:id` | Admin | Delete (409 if rates exist). |
| POST | `/shipping/rates` | Admin | Add rate: `{zone_id, name, rate_cents, free_over_cents?, sort_order}`. |
| PATCH | `/shipping/rates/:id` | Admin | Update rate (no zone_id change). |
| DELETE | `/shipping/rates/:id` | Admin | Remove rate. |

### Zone Matching

- Country ∈ zone.countries AND (zone.regions empty OR state ∈ zone.regions)
- Region-restricted zone **without state never matches**.

### Checkout Integration

`POST /checkout` requires `shipping_rate_id` that resolves to destination → 400 if mismatch.  
Applied cost = `rate_cents` or `0` if `subtotal >= free_over_cents`. Snapshotted into order.

### Tables

- `shipping_zones` (id, tenant_id, name, countries TEXT[], regions TEXT[], created_at)
- `shipping_rates` (id, tenant_id, zone_id, name, rate_cents, free_over_cents, sort_order, created_at)

---

## Discounts (Phase 10)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/discounts` | Admin | List codes. |
| POST | `/discounts` | Admin | Create: `{code, type, value_percent?, value_cents?, min_subtotal_cents?, starts_at?, ends_at?, usage_limit?, status?}`. Type: `percentage` (1–100) or `fixed_amount` (>0). Code normalized uppercase. |
| GET | `/discounts/:id` | Admin | Fetch one. |
| PATCH | `/discounts/:id` | Admin | Update (type switch requires matching value). |
| DELETE | `/discounts/:id` | Admin | Remove (204). |
| POST | `/cart/discount` | Customer | Apply to cart. Body: `{code}`. Validated at apply time; recorded on cart. |

### Cart JSON (discount fields)

```json
{ "discount_code": "SAVE10", "discount_cents": 200 }
```

### Validity Rules (apply + checkout)

- Exists, `status=active`, within `starts_at`/`ends_at`, meets `min_subtotal_cents`, under `usage_limit`.
- Checkout re-validates via `Claim` (`FOR UPDATE` on code row, increments `times_used`).
- Concurrent checkouts: exactly one wins for `usage_limit=1`.

### Table

- `discounts` (id, tenant_id, code, type, value_percent, value_cents, min_subtotal_cents, starts_at, ends_at, usage_limit, times_used, status, created_at) — UNIQUE (tenant_id, code)

---

## Customer Accounts (Phase 11)

### JWT Claims (Customer)

```json
{
  "tenant_id": "uuid",
  "customer_id": "uuid",
  "scope": "customer",
  "exp": 1234567890
}
```

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/customers/signup` | Public | `{email, phone, password}` → `{token, expires, customer}`. Email unique per tenant. |
| POST | `/customers/login` | Public | `{email, password}` → `{token, expires, customer}`. |
| GET | `/customers/me` | Customer | Profile: `{id, email, phone, addresses[]}`. |
| GET | `/customers/me/orders` | Customer | Customer's orders (via `orders.customer_id`; guest orders invisible). |
| GET | `/customers/me/addresses` | Customer | List addresses (default first). |
| POST | `/customers/me/addresses` | Customer | Add: `{label?, address_line1, address_line2?, city, state?, postal_code?, country, is_default?}`. |
| PATCH | `/customers/me/addresses/:id` | Customer | Partial merge. `is_default:true` promotes this, demotes others. |
| DELETE | `/customers/me/addresses/:id` | Customer | Remove (204). |

### Scope Isolation

- Customer JWT on admin route → 403
- Merchant JWT on customer route → 403
- No token on customer route → 401
- Same email on different tenants → distinct customers

### Tables

- `customers` (id, tenant_id, email, phone, password_hash, created_at) — UNIQUE (tenant_id, email)
- `customer_addresses` (id, tenant_id, customer_id, label, address_line1, address_line2, city, state, postal_code, country, is_default)

---

## Notifications (Phase 12)

### Provider Interface

```go
type Provider interface {
    Name() string
    Send(ctx context.Context, n Notification) error
}
```

### Implementations

- `LogProvider` (default) — logs JSON to stdout
- `ResendProvider` — HTTPS POST to `api.resend.com` (requires `RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL`); **the live path in production**
- `SMTPProvider` — Go stdlib `net/smtp`; available, used only if `SMTP_*` vars are set again

### Provider resolution (`main.go`)

Priority: **SMTP → Resend → Log**. SMTP would win the moment `SMTP_HOST` is set (even non-empty). Resend needs `RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL`. Otherwise LogProvider. **Production currently has no `SMTP_*` vars → Resend wins.**

**SMTP env vars:**

| Variable | Example | Default | Notes |
|----------|---------|---------|-------|
| `SMTP_HOST` | `mailpit-p1zaxdgvrrdf9p6bb1czudqi` | — (LogProvider if unset) | presence of this key is the switch |
| `SMTP_PORT` | `1025` | `587` | |
| `SMTP_TLS_MODE` | `starttls` | `starttls` | `starttls` \| `tls` \| `none`; `starttls` falls back to plaintext when the peer doesn't advertise STARTTLS |
| `SMTP_TLS_VERIFY` | `false` | `true` | `false` skips cert verification (Mailpit is self-signed) |
| `NOTIFICATIONS_FROM_EMAIL` | `no-reply@shopkeet.com` | — | envelope sender |

**Shopify-style store addressing** — From/Reply-To are derived per tenant from `APP_BASE_DOMAIN`:

- `From:` `"<StoreName> via Shopkeet <no-reply@<subdomain>.<base>.com>"`
- `Reply-To:` `support@<subdomain>.<base>.com`

Example (live): `MailTest Store via Shopkeet <no-reply@mailtest.shopkeet.com>` / `support@mailtest.shopkeet.com`.

### Event Subscriptions

| Event | Trigger | Notification Type |
|-------|---------|-------------------|
| `order.created` | Checkout completes | `order_confirmation` |
| `order.paid` | Order → `delivered` | `order_delivered` |
| `customers.signup` | Customer registers | `customer_welcome` |

**Handlers always return `nil`** — failed sends never break checkout/status change. Log row status = `sent` or `failed`.

**Events are emitted post-commit, not inside the tx.** The request tx middlewares commit via `auth.commitAndFlush(c, tx, ctx)`, which runs callbacks registered with `auth.AfterCommit(c, fn)` right after `tx.Commit()`. Emitters (checkout `order.created`, status→delivered `order.paid`, customer signup `customers.signup`) schedule the bus emit there so
async notification handlers — which read on a **fresh pool connection** — never race the producing transaction (previously failed with `no rows in result set`). Handlers run with `context.Background()`, not the request ctx.

### Endpoint

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/notifications/log` | Admin | Last 100 delivery attempts: `{id, notification_type, recipient, order_id?, status, sent_at}`. |

### Table

- `notification_log` (id, tenant_id, notification_type, recipient, order_id, status, sent_at)

---

## Store Settings, Order Notes & Tax (Phase 13)

### Tenant Settings Endpoint

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/tenant/settings` | Admin | Returns: `{name, logo_media_asset_id, default_currency, timezone, support_email, support_phone, tax_rate_percent}`. |
| PATCH | `/tenant/settings` | Admin | Partial merge. `tax_rate_percent` validated 0–100. |

### Order Notes

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| PATCH | `/orders/:id/note` | Admin | Set/clear internal note. Body: `{note: "..."}`. |

**Internal note:** Only in admin order responses (`GET /orders`, `GET /orders/:id/status`). Never in customer-facing (`GET /orders/:id?phone=`, `GET /customers/me/orders`).

### Tax Calculation

Tax is calculated on the **discounted** subtotal — the amount the customer actually pays for goods — then shipping is added:

```
tax_cents = (subtotal_cents - discount_cents) * tax_rate_percent / 100
total_cents = (subtotal - discount_cents) + shipping_cost_cents + tax_cents
```

`tax_cents` snapshot stored on order. Order JSON always includes `tax_cents`.

### Tables (columns added)

- `tenants`: `logo_media_asset_id`, `default_currency`, `timezone`, `support_email`, `support_phone`, `tax_rate_percent`
- `orders`: `tax_cents`, `internal_note`

---

## Merchant Operations: Draft Orders & Returns (Phase 15)

Merchants take phone/WhatsApp orders outside the storefront, and customers (or
merchants on their behalf) file returns that restock inventory once received.

### Draft Orders

Phone/WhatsApp sales entered by a merchant. A draft is a **real order**: it runs
the same Phase 4 `FOR UPDATE` stock guard, snapshots line prices, resolves
shipping/tax exactly like checkout, and **decrements inventory the same way a
storefront order does**.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/orders/draft` | Admin | Create a merchant-entered order. Idempotency-guarded (Phase 14). Body matches checkout (same required shipping fields + rate) plus `customer_id?` (attach an existing Phase 11 customer) and `lines:[{variant_id, quantity, unit_price_cents?}]`. `unit_price_cents` 0/omitted = live variant price; otherwise the merchant override is snapshotted. |

Order is stored with `source='draft'` (checkout orders are `source='storefront'`),
status `pending`, payment `cod` by default. `order.created` is emitted with
`source: "draft"`. `GET /orders` (Admin) surfaces `source` on every order.

### Returns & Restock

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/orders/:id/returns` | Customer or Admin | File a return. A customer is verified by `?phone=` (and optional `?email=`) exactly like `GET /orders/:id` — a wrong phone on a random order id sees nothing. A merchant bearer token bypasses the phone check. Body: `{reason?, restock?, items:[{order_item_id, quantity}]}` (restock defaults `true`). Return is born `status='requested'`. |
| GET | `/returns` | Admin | List the tenant's returns, newest first, with items and their product/variant. |
| PATCH | `/returns/:id/status` | Admin | Walk `requested → approved → received → refunded`; `requested/approved` may instead be `rejected`. |

**Marking a return `received` with `restock=true` increments the returned
quantities on the correct `order_items.variant_id`** (never the product as a
whole), refreshes the products aggregates and drops the Redis product detail
cache — the Phase 15 acceptance criterion.

```sql
-- Phase 15
-- orders: source TEXT NOT NULL DEFAULT 'storefront'  ('storefront' | 'draft')
returns            (id, tenant_id, order_id, reason, status, restock, created_at, updated_at)
return_items       (id, tenant_id, return_id, order_item_id, quantity)
```

### Auth

`POST /orders/:id/returns` mounts a single route under `MerchantOrCustomerMW`:
a merchant Bearer token takes the `TenantMW` identity path (`actor=merchant`);
a customer token or no token opens the RLS-scoped transaction (`actor=customer`)
where the phone/email order lookup is the gate. Invalid Bearer tokens fail
closed with 401.

---

## Product Reviews (Phase 16)

Judge.me replacement shipping inside the product surface. A signed-in customer
submits a review; the merchant publishes/rejects it from the admin list; the
product's rating aggregates are recomputed **the moment a review is published**
(never on a pending create).

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/products/:id/reviews` | Customer | Create a review. `{rating (1–5, required), title?, body?, photo_media_asset_ids?}`. If the customer has a **delivered** order containing the product, the most recent one is auto-linked as `order_id` → `verified: true`. One review per (product, order, customer); unverified reviews dedupe per (product, customer). A duplicate → `409`. Review is born `status='pending'`. |
| GET | `/products/:id/reviews` | Public | Published reviews, newest first, plus the product's live `rating_average` / `rating_count`. |
| GET | `/reviews` | Admin | All statuses (approve/reject worklist), newest first, optional `?status=` filter. |
| PATCH | `/reviews/:id` | Admin | `{status}` where status is `published` or `rejected`. Any change recomputes the product aggregate and drops its Redis product-detail cache. |
| DELETE | `/reviews/:id` | Admin | Delete a review; recomputes the product aggregate (`{"deleted": id}`). |

```sql
-- Phase 16
product_reviews   (id, tenant_id, product_id, customer_id NULL, order_id NULL, rating 1–5, title, body,
                   photo_media_asset_ids UUID[] DEFAULT '{}', status pending|published|rejected, created_at)
products          + rating_average NUMERIC(2,1) DEFAULT 0, rating_count INTEGER DEFAULT 0
```

Order of operations (accepted live): create review (pending, verified from the
delivered `orders`×`order_items` join) → merchant `PATCH /reviews/:id` published
→ product rating updates immediately for the storefront → delete drops the
review and recomputes back. Rejected reviews never affect the aggregate.

---

## Abandoned Cart Recovery & Lifecycle Emails (Phase 17)

Klaviyo replacement: an hourly job emails shoppers who left a cart idle (>1h)
with a captured email and no order yet — exactly once per cart.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/cart/email` | Customer (guest session) | Capture the shopper's email for recovery. `{email}` validated; upserts `carts.customer_email` idempotently, echoes the cart with `email` set. |

Every cart mutation refreshes `carts.last_activity_at` (add, patch, delete,
discount, email capture) so an active shopper is never flagged. The scheduled
task `cart:abandonment` (Asynq, `@every 1h`, Phase 14 worker) scans per tenant:

```sql
-- candidate: has an email, not yet recovered, idle >1h, still has items
SELECT id FROM carts
WHERE customer_email IS NOT NULL
  AND recovery_sent_at IS NULL
  AND last_activity_at < now() - interval '1 hour'
  AND EXISTS (SELECT 1 FROM cart_items ci WHERE ci.cart_id = carts.id);
```

Each candidate gets one `cart_abandoned` email (via the Phase 12 provider —
Resend live) with the item list + total + a return-to-cart link, logged in
`notification_log` (`order_id NULL`), then `recovery_sent_at` is stamped so a
second sweep never emails the same cart. A **converted** cart is never a
candidate: checkout deletes the cart + items transactionally (orders.go), so
once an order exists the cart is gone. A cart converts *before* the hour is up
the same way (deleted at checkout) — structurally immune.

```sql
-- Phase 17
carts              + customer_email TEXT, last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                     recovery_sent_at TIMESTAMPTZ
-- index (partial, candidate scan): (tenant_id, last_activity_at) WHERE customer_email IS NOT NULL
--   AND recovery_sent_at IS NULL
notification_log   + type 'cart_abandoned' (order_id NULL)
```

Delivery lifecycles (order shipped/delivered, etc.) beyond the recovery email
remain covered by the Phase 12 order notifications — the recovery sweep is the
only scheduled lifecycle email in v1.

---

## Database Schema Summary (All Phases)

```sql
-- Phase 1
tenants
merchant_users

-- Phase 2
media_assets

-- Phase 3
categories
products
product_categories
product_images

-- Phase 4
carts
cart_items

-- Phase 5
orders
order_items

-- Phase 6
posts
templates
sections
redirects

-- Phase 8
product_options
product_option_values
product_variants
product_variant_option_values
-- cart_items.variant_id, order_items.variant_id (NOT NULL)

-- Phase 9
shipping_zones
shipping_rates
-- orders: shipping_address_line1/2, city, state, postal_code, country, shipping_method, shipping_cost_cents

-- Phase 10
discounts
-- carts.discount_code, orders.discount_code, orders.discount_cents

-- Phase 11
customers
customer_addresses
-- orders.customer_id (nullable)

-- Phase 12
notification_log

-- Phase 13
-- tenants: logo_media_asset_id, default_currency, timezone, support_email, support_phone, tax_rate_percent
-- orders: tax_cents, internal_note

-- Phase 15
-- orders: source TEXT NOT NULL DEFAULT 'storefront' ('storefront' | 'draft')
returns
return_items

-- Phase 16
product_reviews
-- products: rating_average NUMERIC(2,1) DEFAULT 0, rating_count INTEGER DEFAULT 0

-- Phase 17
-- carts: customer_email TEXT, last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
--        recovery_sent_at TIMESTAMPTZ

-- Phase 18
gift_cards
-- carts:  gift_card_code TEXT
-- orders: gift_card_code TEXT, gift_card_cents INTEGER NOT NULL DEFAULT 0

-- Phase 19
-- product_variants: allow_preorder BOOLEAN DEFAULT false, preorder_ships_at TIMESTAMPTZ
-- order_items: is_preorder BOOLEAN DEFAULT false
back_in_stock_subscriptions
-- notification_log: type 'back_in_stock'

-- Phase 22
wishlist_items

-- Phase 23
-- orders: tracking_number TEXT, tracking_carrier TEXT, tracking_url TEXT

-- Phase 24 (indexes only — no new tables)
-- orders_created_at_idx (orders: tenant_id, created_at DESC)
-- order_items_order_idx (order_items: order_id)
-- carts_created_at_idx (carts: tenant_id, created_at)

-- Phase 25 (tables)
bundles
bundle_items
quantity_breaks

-- Phase 26 (table)
product_recommendations

-- Phase 27 (tables)
affiliates
affiliate_commissions
affiliate_payouts
-- orders: affiliate_code TEXT
```

**Every tenant-scoped table has:**
- `tenant_id UUID NOT NULL REFERENCES tenants(id)`
- `ENABLE ROW LEVEL SECURITY`
- `FORCE ROW LEVEL SECURITY`
- `CREATE POLICY tenant_isolation USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)`
- `OWNER TO shopkeet_app`

---

## Auth Scopes & Middleware

| Middleware | Scope Check | Used For |
|------------|-------------|----------|
| `TenantMW(pool, secret)` | Requires `scope="merchant"` | All admin routes |
| `PublicTenantMW(pool)` | No JWT; resolves tenant from `X-Tenant-ID` | Public storefront (products, shipping rates, signup/login) |
| `PublicOrAdminMW(pool, secret)` | JWT optional; if present must be merchant | Dual-role routes (e.g. `GET /products/:id`) |
| `CustomerMW(pool)` | Guest session (cookie/header) | Cart, guest checkout |
| `CustomerAuthMW(pool, secret)` | Requires `scope="customer"` | `/customers/me/*` |
| `CustomerOrGuestMW(pool, secret)` | Customer JWT → link order; else guest session | `POST /checkout` |
| `AffiliateAuthMW(pool, secret)` | Requires `scope="affiliate"` | `/affiliates/me/*` (Phase 27) |

---

## Error Shape (All Endpoints)

```json
{ "error": { "code": "machine_code", "message": "Human readable" } }
```

Default codes by status:
- 400 → `invalid_request`
- 401 → `unauthorized`
- 403 → `forbidden`
- 404 → `not_found`
- 409 → `conflict`
- 502 → `upstream_error`
- 5xx → `internal_error`

---

## Order Tracking & Invoice PDF (Phase 23)

Sent 2026-10-02 (commit `737e2c8`, migration `0025_order_tracking`). The AfterShip /
Sufio replacements in one phase: merchants attach a carrier + tracking number to an
order as it advances to `shipped`, the customer-facing `GET /orders/:id` lookup
surfaces it, and a server-generated A4 `invoice.pdf` with line items and the full
money block is served to both admins and customers — the PDF total is computed from
the exact same columns checkout snapshots, so it can never disagree with
`orders.total_cents`.

### Model

`orders` gains three nullable `TEXT` columns: `tracking_number`, `tracking_carrier`,
`tracking_url`. Not a separate table (a carrier gets 0–N tracking events but the
storefront only needs the latest URL bullet). RLS untouched. The PDF needs **no new
table at all**: it renders from `order_items` (joined to `product_variants` /
`products` for name + SKU) and the order's existing money columns.

### Server-side invoice (new: `github.com/go-pdf/fpdf v0.9.0`)

A4, portrait, page margins 20mm, auto page-break at 25mm bottom. Header: store name
(real `tenants.name`, fallback `Shopkeet`, via RLS) + `INVOICE` + order id, date,
currency. Customer block: name, phone, email (phase 5 columns). Shipping block:
`shipping_address_line1`/`city`/`country` when present. Table: QTY / ITEM (name +
SKU) / UNIT / LINE, then `Subtotal`, `Shipping`, `Discount`, `Gift card`, `Tax`
rows and a bold `TOTAL` — each from `computeInvoice`, which derives from
`total_cents`' own parts (`subtotal + shipping + tax − discount − gift`), so a unit
test can assert equality without any formatting drift. Out-of-Latin-1 runes in
product names are mapped to `?` (core fonts are Latin-1 only); currencies use
symbols for `usd`/`eur`/`gbp`/`pkr`, an ISO code otherwise.

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| PATCH | `/orders/:id/status` | Merchant | Body now also accepts optional `tracking_number`, `tracking_carrier`, `tracking_url`; persisted when provided on any admitted **advance** transition (cancelled excluded). Absent fields are preserved; `tracking_*: ""` clears. Answering with tracking after `pending`→`confirmed`→`delivered` is fine — the dashboard sets it when the carrier label is scanned at `shipped`. |
| GET | `/orders/:id` | Customer | Lookup response now always includes `tracking_number`, `tracking_carrier`, `tracking_url` (`null` until set). Shipping apps watch these three. |
| GET | `/orders/:id/invoice.pdf` | Merchant, **or** Customer (same phone[/email] lookup as the order fetch) | Streams `application/pdf` (byte body, starts `%PDF-`). Admin path loads by id only; customer path requires `?phone=` → `404 "order not found"` on mismatch. Wrong/missing phone → `400`. |

Tracking info is deliberately never scrubbed from customer responses — a shopper
confirms delivery with the carrier link; there is nothing private in a tracking
number. The invoice exposes only what the storefront already shows after checkout
(customer + shipping details + itemized amounts).

---

## Storefront Analytics (Phase 24)

Sent 2026-10-02 (commits `660268c` → `8c125cb`, migration `0026_analytics`). The
LifeTimely / Triple Whale replacement: three merchant-only aggregation endpoints
over the existing orders / order_items / carts data — **no new tables**. Revenue
("sales") means any order whose status isn't `cancelled`; `payment_status` is
deliberately ignored because COD orders are revenue at placement. Everything runs
inside the tenant tx `TenantMW` opened, so RLS scopes the aggregates and no query
ships a `tenant_id` filter twice.

### Model

The spec (§24) demanded an index on `orders.created_at`; it also claimed
`order_items.order_id` was already indexed "via the FK" — false for Postgres, which
does not index the referencing side, so migration `0026` adds explicit indexes
coverings all three queries:

- `orders_created_at_idx` on `orders (tenant_id, created_at DESC)` — the sales /
  conversion window filter.
- `order_items_order_idx` on `order_items (order_id)` — the top-products join.
- `carts_created_at_idx` on `carts (tenant_id, created_at)` — the conversion
  cart-count.

Buckets are UTC `date_trunc('day')` in Go `time.Time` (scanned, then formatted
`2006-01-02`; Postgres `date` → `string` is not a supported pgx scan). Aggregate
`SUM`/`COUNT` are cast `::int` in SQL (they return `bigint`). Two live-debug
fixes landed after first deploy: the `date` scan type, and aliasing the aggregate
columns (`AS quantity`, `AS revenue_cents`) so `ORDER BY` resolves against the
output list instead of an ungrouped input column.

### Endpoints (all Merchant)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/analytics/sales?period=7d\|30d\|90d` | Merchant | `{period, currency, totals:{revenue_cents, order_count}, buckets:[{date, revenue_cents, order_count}]}` — non-cancelled orders, total and per-UTC-day. `period` defaults to `30d`; anything else → `400`. |
| GET | `/analytics/top-products?period=&metric=quantity\|revenue&limit=1-50` | Merchant | `{period, metric, currency, items:[{product_id, product_name, quantity, revenue_cents}]}` — sales grouped by product (a variant sale rolls up to its product via the `order_items.product_id` snapshot), ranked by the requested metric (default `quantity`). Cancelled orders contribute nothing. |
| GET | `/analytics/conversion?period=` | Merchant | `{period, carts_created, orders_placed, conversion_rate}` — funnel of carts present vs. orders placed (incl. cancelled) in the window; `conversion_rate = orders_placed / carts_created` rounded to 4dp. |

Single-currency assumption: `currency` is always `"usd"`. The funnel counts
**carts currently in the table** within the window (a checkout deletes the cart
row, so completed carts fall out of `carts_created`); count is from the carts
table itself — a noted data-model constraint, not something the endpoint hides.

### Verification

- Pure: `TestParsePeriodAndLimit` — period/limit parsing, `400`s.
- Integration (`TestAnalyticsReconciliation`, requires the same DB the suite runs
  against): seeds a tenant with 4 carts, 2 products (cheap Teapot, expensive Mug)
  and 4 orders; asserts the API sales total/buckets reconcile **exactly** against
  a manual `SUM(total_cents)` (cancelled excluded), top-products ranks by the
  requested metric — Teapot wins by quantity, Mug by revenue — and tenant B sees
  zero of tenant A's aggregates under RLS.
- Live smoke (tenant purged after): seeded tenant + a second admin-created product,
  3 guest checkouts (A×3=6500, B×2=10500, A×1 cancelled) + 1 abandoned cart, then
  `period=30d` results cross-checked by hand against psql:
  `sales` 23500/3 ✓ bucket 2026-10-01 ✓ · `top-products` qty: A(6)/B(2) ✓ rev:
  A(12000)/B(10000) ✓ · `conversion` carts 1/orders 4/rate 4 ✓ · bad `period` and
  bad `metric` → `400` ✓.

---

## Product Bundles & Quantity Breaks (Phase 25)

Sent 2026-10-02 (commits `455dbac` + `f7663dc`, migration `0027_bundles`). The
first phase of `09-growth-features-build-spec.md`, replacing the ReConvert /
"bundle apps" tier: merchants compose products into **fixed** bundles (one flat
price for the whole bundle) or **mix-and-match** bundles (a % off whatever the
customer picks from a pool), and set per-product **quantity breaks** (best % off
once a line's quantity clears a threshold). This is the first phase where the
**cart / checkout money math** itself becomes bundle-aware — the shopper pays the
bundle price, not the sum of its variants.

### Model (`0027`)

- `bundles` — `tenant_id`+RLS, `type` (`fixed` | `mix_and_match`), `status`
  (`draft` | `active` | `archived`, default `draft`), and **exactly one** of
  `bundle_price_cents` / `discount_percent` (two CHECKs pin the shapes: `fixed`
  must carry `bundle_price_cents`, `mix_and_match` must carry
  `discount_percent`). `UNIQUE (tenant_id, name)`.
- `bundle_items` — one row per component product (`quantity` = units per bundle
  for `fixed`, marker `1` for `mix_and_match`); `ON DELETE CASCADE` from the
  bundle, `UNIQUE (bundle_id, product_id)`.
- `quantity_breaks` — `product_id`, `min_quantity`, `discount_percent`,
  `UNIQUE (product_id, min_quantity)`; `ON DELETE CASCADE` from the product.
- `cart_items.bundle_id` + `order_items.bundle_id` (nullable, `REFERENCES
  bundles(id)`) — a fixed bundle expands into **one cart row per component
  variant**, each row tagged with the bundle id so the pricers and the order
  snapshot can recognize a bundle group. `order_items` snapshot each component's
  **real** unit price (not the bundle price) because stock must decrement per
  variant.

All three new tables follow the standard contract exactly (tenant_id + `ENABLE` /
`FORCE RLS` + same-migration `tenant_isolation` policy + `OWNER TO shopkeet_app`);
`bundles`/`quantity_breaks`/`bundle_items` get tenant indexes.

### Pricing model (`bundles.PriceCart`, called by cart load + checkout)

- **Bundle lines price as a unit.** `fixed` = `bundle_price_cents` × complete
  sets (the smallest `lineQty / configuredQty` ratio across components, clamped
  ≥ 1 — a shopper who shrank one component line pays for fewer bundles); the flat
  total is allocated across the component lines pro-rata by real value, last line
  absorbing rounding. `mix_and_match` = `discount_percent` off the summed real
  component prices of the lines actually in the cart.
- **Plain lines** get the best qualifying quantity break (highest
  `discount_percent` whose `min_quantity` the line quantity clears). Breaks never
  stack onto bundle lines — a bundle already carries its own discount.
- A bundle referenced by a cart but deleted degrades to the component sum
  (graceful read); **checkout additionally gates** every `bundles.status='active'`
  before an order is formed → `409`. Both cart read and checkout run inside the
  request tx, so RLS scopes every pricing query.

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/bundles` | Merchant | Create a bundle (`name`, `type`, exactly-one pricing field, optional `status`, `items:[{product_id, quantity?}]`). `type` omitted → `fixed`. Mixed shapes (`fixed`+`%`, `mix`+flat, both-price) → `400`; items referencing another/unknown/inactive product → `400`. |
| GET | `/bundles` | Public / Merchant (single route) | List. Public: active bundles only, no `status` field leak. Merchant: all statuses + `status`, optional `?status=` filter. A single `PublicOrAdminMW` route since `9165c9a` — the split public + admin `GET /` pattern let Fiber shadow the admin view (see the Phase 26 hotfix note). |
| GET | `/bundles/:id` | Merchant | One bundle + its items. |
| PATCH | `/bundles/:id` | Merchant | Partial merge; sending `items` replaces them wholesale; any pricing field flips the pricing mode (re-validated on the merged row). `404` across tenants (RLS). |
| DELETE | `/bundles/:id` | Merchant | Delete (items cascade); `409` while any cart/order still references the bundle (`23503` → translated). |
| POST | `/products/:id/quantity-breaks` | Merchant | Create a break; duplicate `(product, min_quantity)` → `409`. |
| GET | `/products/:id/quantity-breaks` | Merchant | List a product's breaks. |
| PATCH | `/products/:id/quantity-breaks/:bid` | Merchant | Update a break. |
| DELETE | `/products/:id/quantity-breaks/:bid` | Merchant | Delete a break. |
| POST | `/cart/bundle` | Customer (guest session) | `{bundle_id, quantity?}` for fixed (1–99; expands the product's **cheapest active variant** per component, rows tagged `bundle_id`) or `{bundle_id, selections:[{product_id, quantity?}]}` for mix (each selection must be in the pool). Adding a bundle whose component variant already sits in the cart outright → `409` (never merges, keeps line pricing intact); draft/archived/deleted → `409`; unknown → `404`; stockless component → `409`. Returns the same cart shape as `POST /cart` — `total_cents` already reflects bundle pricing. |

### Verification

- Integration (`TestBundlesAcceptance`, `internal/bundles`, requires the DB the
  suite runs against): seeds 2 alpha products + 1 beta product + shipping; asserts
  fixed bundle carts at exactly 900 (not the 1500 sum) with every line carrying
  `bundle_id`, checkout totals 1400, per-variant stock decrement, mix discounts
  the whole pick (1350/1850), quantity breaks on the 2× line (900/1400), tenant
  isolation (`404`s + empty public list), draft/archived refused (`409`), and the
  admin validation matrix (cross-tenant item, both-price, mix-with-flat,
  fixed-with-%, type-default).
- Live smoke (tenants purged, prod DB back to 0 bundles): created Duo (fixed 900
  active) + PickMix (mix 10%) + Drafty2; `GET /bundles` (public) = 2 active;
  fixed bundle in cart = **900**, 2 lines, both `bundle_id`-tagged; checkout =
  **1400** with the two `order_items` snapshotted at real prices (1000/500, both
  `bundle_id`-linked); mix cart = **1350**, checkout **1850**; quantity break
  min-2 @ 10% made a 2× alpha-two line **900** (checkout 1400), create + duplicate
  `409` verified; variant stock 5→3 / 10→6 exactly; draft add → `409`; archive →
  hidden (public count 1) + add `409`; beta admin reading alpha's bundle → `404`;
  cross-tenant bundle item → `400`; bad mix selection → `400`.

---

## Upsell, Cross-sell & Post-Purchase Recommendations (Phase 26)

Sent 2026-10-02 (commits `91e1d31` + the shared GET hotfix `9165c9a`/`4a553ef`,
migration `0028_recommendations`; RLS hardening onward in migration `0029`).
Merchants curate **"you may also like /
customers also bought"** links per product (the storefront rail every product
app has), and the order-confirmation screen gets a **post-purchase upsell**: a
pending COD order can add another line while fulfilment hasn't started. Shipped
alongside Phase 25, and the two phases share the route-table fix below.

### Model (`0028`)

`product_recommendations` — `tenant_id` + RLS (`ENABLE`/`FORCE` + same-migration
`tenant_isolation` policy + `OWNER TO shopkeet_app`); `product_id` (the product
whose rail this is) and `recommended_product_id` (the pick) both FK to
`products(id)` **without cascade** — products archive, never hard-delete. `type`
(`manual` = merchant-curated, `auto` = reserved for the Phase 32 scheduled job)
has a CHECK; `sort_order` is the sort key. `UNIQUE (product_id,
recommended_product_id, type)` turns a storefront double-tap into a `23505`.
`recommendations_product_idx` `(product_id, type, sort_order)` serves the rail.

The upsell adds no table — `orders.customer_phone` (Phase 5) is the re-entry key
guest shoppers pass back into `AddOrderItem`.

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/products/:id/recommendations` | Public / Merchant (single route) | The rail. Public storefront: only picks whose recommended product is **active**, no status leak — a dead link never renders. Merchant: every pick incl. archived, each with `status`. Unknown product → `404` on both views. Sorted `type, sort_order, name`. |
| POST | `/products/:id/recommendations` | Merchant | `{recommended_product_id, type?, sort_order?}`. `type` defaults `manual` (`manual`/`auto` only); self-recommend → `400`; both endpoints must be active → `400 "product not found or not active"`; duplicate → `409`; `sort_order < 0` → `400`. Idempotency-guarded; `201` returns the row. |
| DELETE | `/products/:id/recommendations/:rid` | Merchant | Remove one pick, scoped to the route's `product_id`; bogus id → `404 "recommendation not found"`. |
| POST | `/orders/:id/add-item` | Customer (JWT or guest session + `customer_phone`) | Post-purchase upsell. Body `{customer_phone, variant_id, quantity}` (1–99). Locks the order `FOR UPDATE`; it must still be `pending` and its recorded `customer_phone` must match, or the order `404`s; `confirmed`+ → `409 "order can no longer be modified"`. Re-runs the authoritative checkout stock guard (locks variant + product, Phase 19 preorder semantics) → `409 "insufficient stock"` / inactive product. Snapshots the variant's current `unit_price_cents`; prices the new line with **the exact checkout pricer** (quantity breaks / bundles) and bumps `total_cents` by that delta — the order's frozen discount/gift/shipping/tax snapshots never rewrite history. Decrements inventory, refreshes the `products` price/stock aggregates, invalidates the Redis product cache. |

### Verification

- Integration (`TestRecommendationsAcceptance`, `internal/recommendations`, needs
  the DB the suite runs against): seeds alpha + beta tenants, products, variants,
  shipping; asserts create defaults (manual, sort 0), duplicate `409`, self `400`,
  bad type `400`, not-active `400`, public list hides archived with no status,
  admin list shows status + archived, cross-tenant read `404`, delete +
  double-delete `404`, and the full add-item window (missing/wrong phone `404`,
  quantity bounds `400`, insufficient stock `409`, confirmed `409`).
- **Acceptance PASS on 2026-10-02 (post-`0029`, against live prod DB):**
  `TestRecommendationsAcceptance` and `TestBundlesAcceptance` both green via the
  cross-compiled test binaries (`-test.count=1`, `DATABASE_URL` =
  `shopkeet_app`), after the first real run surfaced the reset-to-`''` GUC trap
  below. Two latent assertion bugs in the tests were fixed while exposing it:
  (1) the wrong-phone add-item probe sent no `quantity`, but `AddOrderItem`
  validates quantity **before** the privacy lookup (`orders.go`), so it answered
  `400 quantity must be at least 1` instead of the expected `404` — the probe now
  sends `quantity:1`; (2) the mix-and-match `sel` snippet carried an
  unbalanced `{`, producing malformed JSON (`400 invalid body`) — the brace
  moved so the POST body is `{"bundle_id":…,"selections":[…]}`
- Live smoke (tenants purged, prod DB back to 0 recommendations after teardown):
  full CRUD — create A1→A2 (manual, sort 0) + A1→A3 (sort 10), duplicate `409`,
  self `400`, bogus type `400`; public list = 2 picks, no status
  leak; archive A3 → public hides it (1) while **admin still shows both with
  `status:"archived"`** — exactly the bug this phase shook out; cross-tenant
  public read `404`; delete + double-delete `404`. Post-purchase: cart 1000 →
  checkout **1500** → add 2× alpha-two @500 with the min-2 10% quantity break →
  **2400** → wrong phone `404` → add qty 10 with only 8 left → `409
  insufficient stock` → confirm order → add again `409`. Verified straight from
  the DB: 3 orders, per-variant stock decremented exactly (5→2 / 10→4), and 0
  rows left for the smoke tenant after teardown.

### Hardening: out-of-transaction reads vs the reset custom GUC (migration `0029`)

The first acceptance run of this phase errored with
`invalid input syntax for type uuid: "" (SQLSTATE 22P02)` at the tests' **direct
pool verification queries** (the archive `UPDATE` in the recommendations test
and the post-checkout stock read in the bundles test) — never inside a handler.
Root cause is server-side and reproducible: Go's custom GUC slot starts as
`''`, and after a transaction that ran `set_config('app.current_tenant', X,
true)` the slot **resets to `''` (not NULL) at COMMIT and persists for the rest
of the pooled session**. Any query then evaluated **outside** a transaction hits
the `tenant_isolation` policy `tenant_id = current_setting(...)::uuid` →
`''::uuid` → `22P02`. ROLLBACK does **not** poison; a fresh connection without
the GUC is NULL and silently returns zero rows (fail-open-ish, still isolated).
pgx was cleared (v5.11.0's `CacheStatement` is server-side prepared statement
caching, irrelevant here — the bare `pool.Exec` reproduced the error one
session after a scoped COMMIT). Production was already safe: the only
out-of-transaction pool queries (`auth.go` login subdomain lookup,
`cart.go:562` recovery-sweep tenant list) target the RLS-free `tenants` table.

**Fix (`0029_rls_nullif_policies`, DB 29):** packed every one of the 36
`tenant_isolation` policies down to
`USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid)`
so a stale `''` reads as NULL and the query fails closed instead of erroring.
Applied to prod via the cross-compiled `migrate-linux` binary
(`schema_migrations` = 29, clean); `pg_policies` shows all 36 NULLIF-wrapped.
The acceptance tests' direct verification queries also run inside a tenant-scoped
transaction (`set_config(..., true)`) so they can see the seeded rows under
`FORCE RLS`. All `rec-*`/`bun-*` seeded tenants from the debug + passing runs
were purged leaf-first; orphan sweep across all 36 RLS tables = 0.

### Shared route-table hotfix (shipped with this phase)

Live smoke proved the Phase 25 "public GET + admin group `GET /`"
double-registration pattern is broken under Fiber: the first-registered (public)
route matches every request, so the admin list route was **unreachable in
production** for both `GET /bundles` and `GET /products/:id/recommendations` —
an admin JWT still got the public shape (no `status`, archived picks hidden).
`9165c9a` folds both views into a **single `auth.PublicOrAdminMW` GET** (the
pattern catalog's `GET /products/:id` already uses), branching the handler on
`c.Locals("admin")`; `4a553ef` then fixed a latent scan the un-shadowed route
immediately exposed — the old admin handler read bare `created_at` into a Go
string, which pgx rejects, so `GET /bundles` `500`'d until it used the `::text`
cast the other bundle queries already carry. Public output is byte-for-byte
unchanged across both endpoints.

---

## Affiliate Program (Phase 27)

Commit in progress (migration `0030_affiliates`, DB 30). Single-level affiliate program per
`09-growth-features-build-spec.md §Phase 27`: merchants approve partner applications (setting
a `commission_percent`), partners get their own JWT scope (`affiliate`), and every order that
arrives with `?ref=CODE` (or an `affiliate_code` body field at checkout) books a commission for
that partner — snapped at **discounted goods subtotal** (`subtotal − goods_discount`), i.e.
never on shipping or tax. Commissions are `pending` until the order is `delivered` (the
`order.paid` event both loyalty and affiliates already subscribe to), then `approved`; the
merchant marks a payout `paid` to flip an affiliate's `approved` commissions to `paid`.

### Model

- `affiliates`: id, tenant_id, name, email (UNIQUE tenant), **code** (UNIQUE tenant — the
  canonical code matched by both `?ref=` and the body field), `commission_percent` NUMERIC,
  status (`pending|approved|suspended`), created_at. RLS contract as elsewhere.
- `affiliate_commissions`: id, tenant_id, affiliate_id, order_id, commission_cents, status
  (`pending|approved|paid`), created_at. UNIQUE (affiliate_id, order_id) `ON CONFLICT DO
  NOTHING` — one commission per order per affiliate, and checkout never fails because of
  affiliate bookkeeping.
- `affiliate_payouts`: id, tenant_id, affiliate_id, amount_cents, status (`requested|paid`),
  created_at. A request is rejected (`409`) while one is already `requested`. Paying out
  covers every currently-`approved` commission for that affiliate.
- `orders.affiliate_code TEXT` — snapshot of the credited code (POST-ref arbitrary fetch
  would get sticky).

### Endpoints & scopes

Public (`X-Tenant-ID`-only): `POST /affiliates/apply` (rate-limited, idempotency-guarded),
`POST /affiliates/login` (returns an **affiliate-scoped JWT**; `pending` → `403`). Merchant:
`GET /affiliates`, `PATCH /affiliates/:id/status` (status + optional `commission_percent`
ride-along), `PATCH /affiliate-payouts/:id`. Affiliate JWT: `GET /affiliates/me/dashboard`,
`GET /affiliates/me/commissions`, `POST /affiliates/me/payout-request`. Unknown / inactive
codes are **silently ignored** — never a checkout failure.

### Auth work

`Claims.AffiliateID` + `auth.SignAffiliate`, new `auth.AffiliateAuthMW`, and a tightened
`parseMerchant` (`scope != "merchant"` → reject, was `scope == "customer"`) so affiliate
tokens get a clean `403` on every admin and customer route.

### Routing gotcha (fixed during acceptance)

Registering the `/affiliates` admin group **before** `/affiliates/me/*` let Fiber shadow the
`me` routes with the admin group's `TenantMW` (first-registered route wins an overlapping
prefix family — same class of bug as the Phase 26 shared hotfix). Fix: register `me` first;
the acceptance test asserts an affiliate JWT gets `403 non-merchant` on admin routes and a
merchant JWT gets `403 non-affiliate` on `/affiliates/me/*`.

### Acceptance (live, DB 30)

`TestAffiliateProgram` ran green **as `shopkeet_app`** against prod: apply → dup `409` →
approve 10% → pending-login `403` → approved-login → bogus ref ignored → valid-ref checkout
books pending 200¢ on a 2000¢ subtotal → `delivered` approves → payout-request `201` /
dup `409` → merchant-paid flips to `paid` → dashboard totals reconcile → affiliate JWT `403`
on admin + customer routes → tenant B `404` on tenant A's payout. Six seeded `aff-*` tenants
purged leaf-first; orphan sweep = 0. NOTE: the suite must run as `shopkeet_app` — under the
`shopkeet` superuser RLS is bypassed, so cross-tenant write checks pass vacuously (that
misled the first run into thinking the cross-tenant payout gate leaked).

---

## Metafields / Custom Fields (Phase 28)

Migration `0031_product_metafields` (DB 31). Per-product custom fields, edited only by the
merchant, surfaced read-only to storefront products.

### Model

- `product_metafields`: id, tenant_id, product_id (FK → products), key, value (TEXT), type
  (`string|number|boolean|json`), timestamps. UNIQUE (product_id, key) — one value per key.
  RLS contract as elsewhere: `ENABLE ROW LEVEL SECURITY` + `FORCE ROW LEVEL SECURITY` +
  `tenant_isolation` ALL policy (NULLIF-wrapped), `OWNER TO shopkeet_app`.

### Endpoints & scopes

Merchant (`TenantMW`): `GET /products/:id/metafields`, `PUT /products/:id/metafields/:key`
(body `{value,type}`), `DELETE /products/:id/metafields/:key`. Public: `GET /products/:id`
now embeds `metafields: [{key,value,type}]` in the detail object (sorted by key, cached, no
extra round-trip).

### Routing gotcha (recurring)

The metafields group (`/products/:id/metafields`) must be registered **before** the catalog
`/products/:id` group (first-registered route wins an overlapping prefix family — same class
of bug as Phases 25/26/27).

### Acceptance (live, DB 31)

`TestProductMetafields` green **as `shopkeet_app`**: admin PUT key `size` on product A,
public GET embeds it, admin list shows it, cross-tenant read `404`, bad type `400`,
`DELETE` missing key `404`. A non-UUID id (`/products/does-not-exist/metafields`) must be
`404` not `500` — `productExists` guards with `uuid.Parse` before querying (an invalid uuid
would otherwise fail Postgres cast `22P02` inside the RLS-`NULLIF(tenant_id::uuid)` lookup
and surface as a 500). `mf28-alpha/mf28-beta` tenants purged; orphan sweep = 0.

---

## Bulk CSV Import/Export (Phase 29)

No new tables. Merchant uploads a CSV of products and gets an async job id; polls the job
for a per-line report. Export streams a synchronous CSV of all products.

### Endpoints & scopes

Merchant (`TenantMW`): `POST /products/import` (multipart `file`, `Content-Type:
text/csv`; returns `{job_id}` + `202`), `GET /products/import/:jobId` (`{status,
report:{total,imported,errors:[{line,error}]}}`), `GET /products/export` (UTF-8 BOM CSV,
header `name,slug,description,price,currency,inventory_count,status,sku,image_url`).
Public/admin images untouched.

### Import semantics

Required column: `name`. Optional: `slug,description,price,currency,inventory_count,status,sku,image_url`.
Per-row: savepoint → sanitize → slug auto-derived (`slugify`, deduped within the batch:
`dup-thing`, `dup-thing-2`) → insert product + default variant → unique-violation on slug
→ `"slug already exists"` row error, savepoint rollback (bad row never aborts the batch).
`status` default `draft`; must be `draft|active|archived` (invalid → line error naming the
offending value). Each product gets a default variant (sku = row sku if given).

### Job plumbing

Reuses the existing Asynq queue: `TaskTypeProductImport`, `EnqueueResult` (returns the
asynq task id), and a worker handler that writes a JSON `Report` to the task result via
`Task.ResultWriter().Write(...)`; `JobStatus` maps TaskState → `pending|processing|done|failed`
and polls with `Inspector.GetTaskInfo`, reading `Result` bytes. Retention option is
`asynq.Retention(d)` — v0.26.0 has no `asynq.ResultTTL`.

### Routing gotcha (again)

`/products/import`, `/products/import/:jobId`, `/products/export` are registered **before**
catalog's `/products` group, so `import`/`export` are never swallowed by `GET /products/:id`.

### Acceptance (live, DB 31)

`TestProductCSVImportExport` green **as `shopkeet_app`**: 4-row CSV (alpha/beta/gamma +
a `badstatus` row) → report shows `errors:[{Line:4, Error:"invalid status \"badstatus\"
(draft|active|archived)"}]`; the two "Dup Thing" rows land as `dup-thing`/`dup-thing-2`
(asserted as a set — random uuids make created_at/id order non-deterministic); `GET
/products/export` returns a BOM CSV whose rows header + imported products. `bulk29-*` tenant
purged; orphan sweep = 0.

---

## Product Feeds (Phase 30)

No new tables. Public, tenant-scoped feed endpoints (`PublicTenantMW`, `X-Tenant-ID`) for
Google Shopping XML and Meta (Instagram/Facebook) CSV.

### Endpoints & scopes

`GET /feeds/google-shopping.xml` — `rss`/`channel` with one `<item>` per **active** product:
`g:id`, `g:title`, `g:description`, `link` (storefront URL), `g:price` (`"45.00 USD"`),
`g:availability` (`in_stock`/`out_of_stock` by `inventory_count > 0`), `g:condition`
(`new`), first image as `g:image_link`. `GET /feeds/meta-catalog.csv` — header
`id,title,description,link,image_link,availability,price,condition` with `45.00_USD` /
`in stock` / `new`. Storefront base = `custom_domain` if set else
`https://{subdomain}.{appBaseDomain}` (per-tenant app_base_domain from settings). Draft and
archived products omitted; XML escapes titles/descriptions; prices use the half-even cents→currency
formatter shared with orders.

### Acceptance (live, DB 31)

`TestProductFeeds` green **as `shopkeet_app`**: XML contains the active+in-stock product with
`<g:price>45.00 USD</g:price>` and `in_stock`, the active out-of-stock product gets
`out_of_stock`, the draft product is absent; CSV rows match; missing `X-Tenant-ID` → `400`.
`feed30-*` tenant purged; orphan sweep = 0.

---

## Wishlist (Phase 22)

Sent 2026-10-01 (commit `c896d8e`, migration `0024_wishlist`). **Wishlist Plus**-type
app replacement: a signed-in customer's saved products, per tenant, with the usual
RLS contract. No guest wishlists — the surface is customer-JWT only, same
`/customers/me` group as Phase 11. Small, low-risk phase, shipped end to end in one
sitting.

### Model

`wishlist_items` — `tenant_id` + `customer_id` + `product_id` + `created_at`, with
`UNIQUE (customer_id, product_id)`. The unique row makes a storefront double-tap a
relational hash join away: the duplicate add surfaces as a `23505` the API answers
`409`, not a second row. `ENABLE/FORCE ROW LEVEL SECURITY` + same-migration
`tenant_isolation` policy + `OWNER TO shopkeet_app`, like every tenant-scoped table.

### Endpoints (all Customer-scoped)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/customers/me/wishlist` | Customer | Every saved product, newest first, joined to live product data (`product_name`, `product_slug`, `price_cents`, `currency`, `status`). |
| POST | `/customers/me/wishlist` | Customer | Body `{product_id}`. Adds to wishlist; responds `201` with the item. Duplicate → `409 "already in wishlist"`. Unknown product → `404`. Missing/empty `product_id` → `400`. |
| POST | `/customers/me/wishlist/:productId` | Customer | Route-param form of the add. |
| DELETE | `/customers/me/wishlist/:productId` | Customer | Removes by product; `204` on success, `404 "not in wishlist"` if absent (a delete that hits nothing is a bug the storefront should see). |

Products of any status can be wishlisted (a merchant may draft/archive a saved
item); the joined `status` lets the frontend grey it out. Isolation is inherited:
tenant B's customer can't add tenant A's product (RLS + FK), and a merchant token is
refused by `CustomerAuthMW` (`403`).

---

## Advanced & Automatic Discounts (Phase 21)

Sent 2026-10-01 (commits `b99665e` → `087da2c`). Bold Discounts replacement:
discounts can now be **automatic** (`requires_code=false`) — a store-wide promo or
"free shipping over $X" — that apply at checkout without any code. When a shopper
also enters a code, checkout applies **exactly one of them: whichever saves more**
(v1 never stacks), and only the winner's `times_used` is bumped so a beaten code or
promo is never burned. Backed by migration `0023_discount_automatic`, folded into
the RLS model like every prior phase.

### Model

`discounts` gains:

- `applies_to TEXT NOT NULL DEFAULT 'order'` — `'order'` discounts the goods
  subtotal (tax computed on the reduced base), `'shipping'` discounts only the
  shipping cost (taxable base untouched; the order still records the original
  `shipping_cost_cents` and the total savings in `discount_cents`).
  `'product'` (BOGO) is reserved: the CHECK constraint permits it but validation
  rejects it with `400 "product (BOGO) discounts are not supported yet"`, as are
  any non-null `buy_quantity` / `get_quantity`.
- `requires_code BOOLEAN NOT NULL DEFAULT true` — `false` marks an automatic
  discount. Automatic discounts are stored with `code NULL` and are **not**
  resolvable by `POST /cart/discount` or claimable as a code (they exist outside
  the cart-code path). A code is only mandatory when `requires_code=true`.
- `buy_quantity` / `get_quantity INTEGER` — nullable BOGO columns reserved for a
  future phase.

### Endpoints (same admin surface as Phase 10, extended)

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/discounts` | Merchant | Now accepts `applies_to` (`order` default, `shipping`) and `requires_code` (default `true`). `requires_code:false` creates an automatic discount — no `code`, never entered by a shopper. |
| PATCH | `/discounts/:id` | Merchant | Same field set; absent fields preserved. |
| GET | `/discounts` · `/discounts/:id` | Merchant | Responses include `applies_to`, `requires_code`, `code` (`null` for automatic), `buy_quantity`/`get_quantity` (`null`). |

### Checkout behavior

1. If the cart carries a code, it is **resolved** (read-only) to `codeCents`.
2. All eligible automatic discounts are scanned
   (`status='active'`, `requires_code=false`, inside the window, under `usage_limit`)
   under `FOR UPDATE` locks, `ORDER BY id`; each is validated against the goods
   subtotal (`min_subtotal_cents` is compared to the cart goods subtotal regardless
   of scope — "free shipping over $X" triggers on what the shopper spends on goods),
   and its cents computed on the subtotal (`order`) or on the shipping cost
   (`shipping`). The **single best-value** winner is picked; **ties -> lowest id**,
   so the outcome is deterministic and never order/index dependent.
3. Winner = better of `auto.DiscountCents` vs `codeCents`. The winner is **claimed**
   (re-validated in the same transaction, `times_used + 1`, exactly-once like the
   code path); the loser is left untouched. Checkout fails closed if the claimed
   discount no longer validates.
4. `discount_cents` on the order = goods savings + shipping savings; the shipping
   rate is snapshot as its original cost, so the customer's effective shipping is
   `rate − shippingDiscount`.

### Migration (`0023_discount_automatic`)

```sql
ALTER TABLE discounts ADD COLUMN applies_to TEXT NOT NULL DEFAULT 'order';
ALTER TABLE discounts ADD COLUMN buy_quantity INTEGER;
ALTER TABLE discounts ADD COLUMN get_quantity INTEGER;
ALTER TABLE discounts ADD COLUMN requires_code BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE discounts ALTER COLUMN code DROP NOT NULL;      -- automatic discounts have no code
ALTER TABLE discounts ADD CONSTRAINT discounts_applies_to_check
  CHECK (applies_to IN ('order', 'shipping', 'product'));
```

Down-migration pre-suffixes NULL codes with `'AUTO-' || left(replace(id::text,'-',''), 8)`
before restoring `NOT NULL`, then drops the new columns and constraint.

### Implementation

`apps/api/internal/discounts/discounts.go`:
- `discountRow.code` is now `*string`; `scanDiscount`/`toJSON` (`strp` helper)
  handle the nullable code; `discountSelect` covers the 16 columns.
- `validateFields` verifies scope, BOGO guard, code requirement, and discount value
  against the merged row; `CreateDiscount`/`UpdateDiscount` carry the new fields
  (create defaults `applies_to` to `order` when omitted).
- `Resolve`/`Claim` (code path) return `"discount code not found"` for
  `requires_code=false` rows, so automatic discounts can never be entered or
  double-claimed.
- New `AutoPick` (scan + validate + best-single-value pick, `FOR UPDATE`, no side
  effects) and `ClaimAuto` (re-validate + `times_used + 1`), plus `AutoQuote`.

`apps/api/internal/orders/orders.go` (checkout): resolve code → `AutoPick` →
pick winner → `ClaimAuto`/`Claim` the winner → split goods vs shipping savings →
tax on `(subtotal − goodsDiscount) * rate / 100` → `amountDue = subtotal −
goodsDiscount + (shipping − shippingDiscount) + tax`.

### Acceptance criteria (live smoke passed 2026-10-01, commit `087da2c`)

- Automatic free-shipping-over-$15 applies with **no code**: 2000¢ cart → 500¢
  off shipping, order total 2000¢, `discount_code` null, shipping snapshot 500¢.
- Code vs automatic: `SAVE30` (−600¢) beats the −500¢ promo → exactly SAVE30
  applies (600¢, total 1900¢) and the promo is not bumped. `SMALL5` (−100¢)
  loses → exactly the promo applies (500¢, total 2000¢) and `SMALL5.times_used`
  stays 0.
- Below the promo's `min_subtotal_cents` (1000¢ < 1500¢): no discount, total 1500¢.
- `usage_limit=1` automatic promo: first checkout applies it (600¢), second falls
  back to the next-best unlimited promo (500¢); `times_used` counted per claim.
- `applies_to:'product'` (BOGO) → 400; admin list/create echo
  `applies_to`/`requires_code`.
- Integration suite: `TestAutomaticDiscounts` (`internal/discounts`, requires the
  migrated local test DB; run with `go test ./internal/discounts/`).

## Loyalty & Referrals (Phase 20)

Sent 2026-10-01 (commit `caab28d`). Smile.io / Gameball replacement: customers
earn points when a delivered order (`order.paid`) is credited, redeem points for
a one-time cart discount, and recruit friends with a per-customer referral code.
Backed by migration `0022_loyalty_referrals`, which folded cleanly into the RLS
model (all new tables FORCE RLS, owner `shopkeet_app`).

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/customers/me/loyalty` | Customer | Running `balance` (points) + recent ledger (newest first, ≤100): each entry `{id, points (+earned/−redeemed), reason, order_id?, created_at}`. |
| GET | `/customers/me/referral` | Customer | Lazily creates the caller's unique `fixed_amount` referral code (value = package constant `ReferralDiscountCents` = 500, `usage_limit` NULL) and returns `{code, value_cents}`. Stable per customer — re-calling returns the same code. |
| POST | `/loyalty/redeem` | Customer | Body `{points}`. Converts points into a one-time `fixed_amount` code (`usage_limit=1`, random code) of `floor(points/rate) × 100` cents applied to the current cart. Returns `{code, discount_cents, points, balance}`. Rejects: `points < 1`, rate ≤ 0 ("redemption disabled"), `points > balance` ("insufficient"), or `points` not reaching one full currency unit. Idempotency-guarded (`POST /loyalty/redeem`). |

### Earning model

- **When:** an order transitions to `delivered` — the existing Phase 12
  `order.paid` event. The buyer earns only if the order is linked to a customer
  account (`customer_id`); guest orders earn nothing.
- **How many:** `floor(total_cents / 100) × loyalty_points_per_currency_unit`.
  Default belongs to each order's tenant; 0 = program disabled for that tenant.
- **Exactly-once:** partial unique indexes
  `loyalty_ledger_order_placed_once ON (order_id) WHERE reason='order_placed'` and
  `loyalty_ledger_referral_once ON (order_id) WHERE reason='referral'`. The
  subscriber inserts with `ON CONFLICT DO NOTHING` and bumps
  `customers.loyalty_points` **only when `RowsAffected()==1`** — a re-emitted
  `order.paid` can never double-credit (verified live + in tests).

### Referral flow

1. Shopper GETs `/customers/me/referral` → `REF-style` fixed_amount code whose
   `discounts.customer_id` points back at them (created lazily, once).
2. Any shopper (guest or account) applies the code at checkout like a normal
   discount.
3. When that order reaches `delivered`, the **referrer** earns the same points
   the buyer's `order_placed` credit would have produced (same rate/formula).
   Self-referral (a customer using their own code) pays out nothing.
4. Redeemed one-time codes carry `customer_id` NULL, so they are never mistaken
   for referral codes by the `order.paid` subscriber.

### Migration (`0022_loyalty_referrals`)

```sql
ALTER TABLE customers ADD COLUMN loyalty_points INTEGER NOT NULL DEFAULT 0;

CREATE TABLE loyalty_ledger (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID NOT NULL REFERENCES tenants(id),
  customer_id UUID NOT NULL REFERENCES customers(id),
  points      INTEGER NOT NULL,           -- positive = earned, negative = redeemed
  reason      TEXT NOT NULL,              -- 'order_placed' | 'referral' | 'redeemed' | 'signup_bonus'
  order_id    UUID REFERENCES orders(id),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE loyalty_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE loyalty_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON loyalty_ledger
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE loyalty_ledger OWNER TO shopkeet_app;
CREATE INDEX loyalty_ledger_customer_idx ON loyalty_ledger (customer_id, created_at DESC);
CREATE UNIQUE INDEX loyalty_ledger_order_placed_once
  ON loyalty_ledger (order_id) WHERE order_id IS NOT NULL AND reason = 'order_placed';
CREATE UNIQUE INDEX loyalty_ledger_referral_once
  ON loyalty_ledger (order_id) WHERE order_id IS NOT NULL AND reason = 'referral';

ALTER TABLE tenants
  ADD COLUMN loyalty_points_per_currency_unit INTEGER NOT NULL DEFAULT 0, -- 0 = disabled
  ADD COLUMN loyalty_redemption_rate INTEGER NOT NULL DEFAULT 100;         -- points per 1 unit of discount
ALTER TABLE discounts ADD COLUMN customer_id UUID REFERENCES customers(id);
```

Exposed through the Phase 13 admin surface: `GET/PATCH /tenant/settings` now
return and accept `loyalty_points_per_currency_unit` and `loyalty_redemption_rate`
(must be ≥ 0; setting the earn rate to 0 disables the program, setting the
redemption rate to 0 disables redemptions).

### Implementation

`apps/api/internal/loyalty`:
- `loyalty.go` — `Service{pool}`; `Subscribe(bus)` registers `onOrderPaid`
  (own RLS-scoped tx like notifications' senders, never fails the status change);
  `GetLoyalty`, `GetReferral`, `Redeem` all run inside the customer's
  `CustomerAuthMW` request tx. Codes generated from crypto randomness, retried on
  the `(tenant_id, code)` uniqueness violation.
- `routes.go` — `GET /customers/me/loyalty`, `GET /customers/me/referral` under
  `CustomerAuthMW`; `POST /loyalty/redeem` under `CustomerAuthMW` +
  `idempotency.Middleware("POST /loyalty/redeem")`.
- `cmd/api/main.go` — `loyaltySvc := loyalty.New(pool)`, `loyaltySvc.Subscribe(bus)`,
  `loyalty.RegisterRoutes(v1, pool, cfg.JWTSecret, loyaltySvc)` (after customers).

### Acceptance criteria (live smoke passed 2026-10-01, commit `caab28d`)

- Fresh customer: balance 0, empty ledger.
- Signed-in 2500¢ COD order → delivered → balance 250 (25 × 10), ledger entry
  `order_placed +250`. Re-broadcasting `order.paid` for the same order did not
  double-credit.
- `GET /customers/me/referral` → minted value 500; second call returns the same
  code.
- Redeem over balance → 400; redeem 150 → `{code, discount_cents:100, points:150,
  balance:100}`, ledger `redeemed -150`; over-balance and sub-unit redemptions 400;
  rate 0 → 400 "redemption disabled".
- Referred buyer (2000¢ after −500 referral coupon) → delivered → buyer earned 200
  and referrer earned a 200 `referral` payout; referrer balance 300.
- Integration suite: `TestLoyaltyReferrals` (`internal/loyalty`, requires the
  migrated local test DB; run with `go test ./internal/loyalty/`).

## Phase 19 — Pre-orders & Back-in-Stock Alerts

Pre-orderable variants can be checked out with zero stock; shoppers can subscribe to
out-of-stock, non-preorderable variants and receive exactly one email when the
merchant restocks.

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/products/:id/variants/:variantId/notify-me` | Public | Subscribe to restock alert. Body: `{email}`. 201 created, 409 "already subscribed" (per-variant, case-insensitive email). 400 if variant is preorderable, in stock, or not found. |
| POST | `/products` | Admin | Create product. Body now accepts `allow_preorder` (bool) and `preorder_ships_at` (RFC3339, optional) on the initial variant. |
| POST | `/products/:id/variants` | Admin | Create variant. Body: `{..., allow_preorder?, preorder_ships_at?}`. |
| PATCH | `/products/:id/variants/:variantId` | Admin | Update variant. Body: `{..., allow_preorder?, preorder_ships_at?}`. Restock (0 → positive inventory) emits `variant.restocked` event after commit. |
| POST | `/checkout` | Customer | Guest or customer JWT. If line has `allow_preorder=true` and stock < quantity, line is marked `is_preorder=true`, no stock decrement, order proceeds. |

### Variant JSON (detail)

```json
{
  "id": "...",
  "option_values": [{"option_id": "...", "value_id": "..."}],
  "sku": "ABC-123",
  "price_cents": 1999,
  "inventory_count": 0,
  "weight_grams": 200,
  "status": "active",
  "allow_preorder": true,
  "preorder_ships_at": "2026-11-01T09:00:00Z"
}
```

### Checkout pre-order behavior

- If `variant.allow_preorder` AND `inventory < quantity` → line becomes a **pre-order** (`order_items.is_preorder = true`).
- Pre-order lines **do not decrement** `product_variants.inventory_count`.
- Non-preorder lines with insufficient stock → 409 as before.
- `order_items` snapshots `is_preorder` at checkout time.

### Back-in-stock flow

1. Shopper POSTs `/products/:id/variants/:variantId/notify-me` (public) with email → 201 subscription recorded in `back_in_stock_subscriptions` (FORCE RLS, owner `shopkeet_app`).
2. Merchant PATCHes variant with `inventory_count > 0` (was 0) → restock detected, `variant.restocked` event emitted via `auth.AfterCommit`.
3. Notifications subscriber (`onVariantRestocked`) runs `deliverBackInStock` goroutine:
   - Claims each unnotified subscription with `UPDATE ... SET notified_at = now() WHERE id = $1 AND notified_at IS NULL` (rows_affected == 1 ⇒ exactly once).
   - Sends email via provider (`notification_type = 'back_in_stock'`).
   - Logs `notification_log` row (`status = 'sent'` or `'failed'` — failed still stamps claim, never re-emails).
3. Subscriber with notified_at stamp is removed from future alerts; restocked variant now rejects notify-me (400 "variant is in stock").

### Tables

```sql
-- Phase 19
ALTER TABLE product_variants
  ADD COLUMN allow_preorder BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN preorder_ships_at TIMESTAMPTZ;

ALTER TABLE order_items
  ADD COLUMN is_preorder BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE back_in_stock_subscriptions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  variant_id UUID NOT NULL REFERENCES product_variants(id) ON DELETE CASCADE,
  email TEXT NOT NULL,
  notified_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE back_in_stock_subscriptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE back_in_stock_subscriptions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON back_in_stock_subscriptions
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
ALTER TABLE back_in_stock_subscriptions OWNER TO shopkeet_app;
CREATE UNIQUE INDEX back_in_stock_subscriptions_email_key
  ON back_in_stock_subscriptions (tenant_id, variant_id, lower(email));
-- notification_log: type 'back_in_stock' added
```

### Events (internal bus)

| Event | Trigger | Payload |
|-------|---------|---------|
| `variant.restocked` | PATCH moves inventory 0 → positive | `{variant_id, product_id, tenant_id}` |

Handler: `notifications.onVariantRestocked` → `deliverBackInStock`.

### Acceptance criteria (live smoke passed 2026-09-28, commit `1eb5c4f`)

- Preorder variant (0 stock, `allow_preorder=true`) → checkout 201, `is_preorder=true`, inventory stays 0.
- Out-of-stock non-preorder variant → notify-me 201 (dup 409, second email 201).
- Cross-tenant notify-me on variant → 404.
- Restock PATCH → `variant.restocked` → subs `notified_at` stamped, `notification_log` rows `back_in_stock|failed` (Resend rejects `example.com` in smoke; real emails use valid domains → `sent`).
- Notify-me on restocked variant → 400 "variant is in stock".
- Exactly-once verified: concurrent restock events → each sub emailed once (claim UPDATE guard).

**Hardening deploy (2026-09-29, commit `c7f61fa`) — live event-path string corruption fixed.** Root
cause: `tenant_id` values stored into `AfterCommit` event payloads came from `c.Get("X-Tenant-ID")` /
`c.Params(...)`, which alias fasthttp's per-request header/buffer memory that is reused once the
request completes. The bus goroutines read those strings milliseconds later and could see silently
mutated values — live evidence was a tenant UUID that arrived as `gzip480e-...`, so the post-commit
`order_confirmation` notification-log insert threw `invalid input syntax for type uuid`. Fix:
`strings.Clone` at every tenant-id read in the auth middlewares (`TenantMW`, `publicTenantMW`,
`PublicOrAdminMW`, `MerchantOrCustomerMW`, `CustomerMW`, `CustomerAuthMW`, `CustomerOrGuestMW`) and at the
`order.paid` / `variant.restocked` emit sites. Re-verified live on the `c7f61fa` image: guest COD
checkout → `order_confirmation` **`sent` with the exact smoke-tenant UUID** (no corruption), restock
PATCH → both subscribers' `back_in_stock` rows **`sent` once** with `notified_at` stamped and
`tenant_id` correct; smoke tenants purged, prod data untouched.

---

## Gift Cards (Phase 18)

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/gift-cards` | Admin | List cards, newest first (code + balances + status + expiry). |
| POST | `/gift-cards` | Admin | Issue: `{code?, amount_cents, expires_at?}`. `amount_cents` > 0 required. Empty code → generated `GC-XXXXXXXX`. Duplicate code in tenant → 409. Idempotency-guarded. |
| POST | `/cart/gift-card` | Customer | Apply to cart. Body: `{code}`. Code uppercased/trimmed; validated and recorded on the cart. Rate-limited 20/hr. Idempotency-guarded. |

There is no customer "remove" endpoint — applying another code replaces it. Re-applying an
exhausted/disabled/expired card is 400, so the cart keeps its previous code.

### Cart JSON (gift card fields)

```json
{ "gift_card_code": "GC-1A2B3C4D", "gift_card_cents": 1000 }
```

`gift_card_cents` on the cart is a *preview*: `min(balance, max(subtotal - discount + shipping + tax, 0))`.

### Checkout Integration

- `amountDue = subtotal - discount_cents + shipping_cost_cents + tax_cents`; the claim is
  `min(balance, amountDue)`.
- `total_cents = amountDue - gift_card_cents`, **floored at 0** — a card larger than the order
  pays it in full and the remainder stays on the card.
- The claim runs **inside the order transaction**: `SELECT ... FOR UPDATE` on the card row, then
  `UPDATE ... SET balance_cents = balance_cents - $1`. Row lock ⇒ serialised claims.
- Order snapshots `gift_card_code` + `gift_card_cents`; later balance changes never re-open a
  placed order. Checkout is the only writer of `orders.gift_card_cents`.
- **Acceptance (concurrent double-spend):** two checkouts racing for the last $5 of a $5 card —
  exactly one succeeds, the other 400s. Covered by `TestGiftCardConcurrentDoubleSpend`.

### Table

- `gift_cards` (id, tenant_id, code, initial_balance_cents, balance_cents, status, expires_at, created_at) — UNIQUE (tenant_id, code), CHECKs: `initial_balance_cents > 0`, `balance_cents >= 0`, `status IN ('active','disabled')`. FORCE RLS + tenant policy + `OWNER TO shopkeet_app`, same as every tenant table.

---

## Env Vars & Config

| Variable | Required | Phase | Description |
|----------|----------|-------|-------------|
| `DATABASE_URL` | Yes | 0 | Postgres as `shopkeet_app` (non-superuser) |
| `REDIS_URL` | No | 4 | Cart reservation + product cache-aside + rate limiting (falls back to Noop: reservation disabled, reads always fresh, limits in-memory) |
| `JWT_SECRET` | Yes | 1 | HS256 signing key |
| `PORT` | No (3001) | 0 | HTTP port |
| `APP_BASE_DOMAIN` | No | 1 | Tenant subdomain base (e.g. `shopkeet.com`) |
| `METRICS_TOKEN` | No | 7 | Bearer token for `/metrics` |
| `R2_ACCOUNT_ID` | Set together | 2 | Cloudflare R2 credentials |
| `R2_ACCESS_KEY_ID` | Set together | 2 | |
| `R2_SECRET_ACCESS_KEY` | Set together | 2 | |
| `R2_BUCKET_NAME` | Set together | 2 | |
| `R2_PUBLIC_URL` | Set together | 2 | Public base for media URLs |
| `RESEND_API_KEY` | Yes | 12 | Transactional email (Resend) — the **live** provider since 2026-09-26 (no `SMTP_*` vars set) |
| `NOTIFICATIONS_FROM_EMAIL` | Yes | 12 | From address for notifications (SMTP envelope + Resend sender); live value `Shopkeet <no-reply@mail.shopkeet.com>` |
| `SMTP_HOST` | No | 12 | SMTP server host — **removed from live env** (2026-09-26); re-adding it would override Resend |
| `SMTP_PORT` | No | 12 | SMTP port (default `587`) — `1025` for Mailpit |
| `SMTP_TLS_MODE` | No | 12 | `starttls` (default) \| `tls` \| `none` |
| `SMTP_TLS_VERIFY` | No | 12 | `true` (default) verify cert; `false` for Mailpit/self-signed |

---

## Live Deployment Status (VPS 13.61.125.59) — 2026-09-26

### Running under Coolify (single source of truth for the API)

> **Deploy decision (re-confirmed 2026-09-25):** Coolify is the deploy layer — the frontend will run on it too, so there's no Vercel and one management surface. This reverses the original compose+Caddy-for-RAM preference (see `shopkeet-agents-package/.agents/rules/backend-constraints.md` and `docs/03-architecture.md` §7). Caddy and its on-demand-TLS redesign for merchant custom domains is **deferred**, not dropped.

| Resource | Identifier | Status |
|----------|-----------|--------|
| Coolify app `shopkeet-api` | uuid `l6modsyezs1vlrv6ly1oqz4i` | **running:healthy** |
| Live commit | `3923cfd` (`main`) | container `l6modsyezs1vlrv6ly1oqz4i-062714743602` (Phase 9 shipping zone fix; **live DB role = `shopkeet_app`, RLS now enforced**, 2026-09-28); prior: `f4ece4c` (notifications log repair), `19fb065` (Phase 18 tenant-scope fix), `a5a0dd3` (Phase 18 gift cards), `512cad0` (Phase 17 recovery fix) |
| Domain | `https://api.shopkeet.com` | 200 (`/healthz` → `{"status":"ok"}`), TLS via Coolify proxy |
| Source | `Theusama1183/shopkeet-backend`, branch `main` | build pack `dockerfile`, `base_directory /apps/api`, `dockerfile_location /Dockerfile`, `ports_exposes 3001` |
| Auto-deploy | `is_auto_deploy_enabled=true` | pushes to `main` trigger builds (webhook; fallback: `POST /api/v1/deploy?uuid={uuid}&force=true` — do **not** use `/applications/{uuid}/start` or `/applications/{uuid}/deploy`, both 404; `/applications/{uuid}/restart` restarts without rebuild) |
| Env | 12 vars incl. `DATABASE_URL`, `REDIS_URL`, JWT/R2/METRICS + `APP_BASE_DOMAIN=shopkeet.com` + `RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL` | `PORT` unset → default 3001; `SMTP_*` vars **removed** (2026-09-26) so Resend is the active provider |
| ⚠️ `DATABASE_URL` role | **`postgres://shopkeet_app:shopkeet_app@shopkeet-postgres:5432/shopkeet?sslmode=disable`** | The API ran as `shopkeet` (a **SUPERUSER**, `rolsuper = t`) until 2026-09-28, which meant **`FORCE ROW LEVEL SECURITY` was bypassed in production and every `tenant_isolation` policy was inert** — `set_config('app.current_tenant', …)` scoped nothing. Both the prod env (`is_preview=false`) and the unused Coolify **preview** env (`is_preview=true`, pointing at Coolify's own `postgres`/`redis` aliases) were changed to the non-superuser `shopkeet_app`, which already owned all 28 tenant tables + every sequence and ran migration 0020. RLS is now genuinely enforced; the pre-flip env is in the deploy log if a rollback is ever needed. |
| `REDIS_URL` | **`redis://shopkeet-redis:6379`** (fixed 2026-09-26) | was wrongly `redis://redis:6379` → resolved to Coolify's own auth'd `coolify-redis` on the `coolify` network, all Asynq ops `NOAUTH` + cache/rate-limit silently dead; fixed via `PATCH /api/v1/applications/{uuid}/envs` |

### Notifications & email (Phase 12, live via Resend 2026-09-26)

- Provider resolution in `main.go`: **SMTP** (`SMTP_HOST` ≥ 1 var) → **Resend** (`RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL`) → **LogProvider**. Production has **no** `SMTP_*` vars, so **ResendProvider is live**: container log shows `notifications: using Resend provider`.
- ✅ **Resend = production mail relay.** Sending domain `mail.shopkeet.com` added + DNS-verified in Resend. Env: `RESEND_API_KEY` set, `NOTIFICATIONS_FROM_EMAIL=Shopkeet <no-reply@mail.shopkeet.com>`. All 4 `SMTP_*` (Mailpit) vars deleted from the live env (deploy `uika6xnucvdpaleelsitbbba`, 2026-09-26).
- E2E verified live through Resend: `POST /customers/signup` → `customer_welcome` to a real inbox; `notification_log` row `status=sent`. Delivery proof lives in the Resend dashboard / inbox (the log stores no provider message id).
- The `Provider` interface swap seam means Mailpit/SMTP can be restored later by re-adding `SMTP_*` vars — no code change. Mailpit itself remains a perfectly good dev-time catcher.
- Shopify-style addressing (per store): `From: "<StoreName> via Shopkeet <no-reply@<subdomain>.shopkeet.com>"` (strictly the verified `mail.shopkeet.com` envelope via Resend; per-store subdomains are not Resend-verifiable), `Reply-To: `support@<subdomain>.shopkeet.com`` (base domain from `APP_BASE_DOMAIN`). `ResendProvider` sends `reply_to` from the notification payload (commit `03d4d61`).
- Phase 12 E2E matrix previously verified against Mailpit (customers.signup → `customer_welcome`, checkout → `order_confirmation`, status → `delivered` → `order_delivered`; all `notification_log` rows `status=sent`) — provider-agnostic, still valid.
- Two latent bugs fixed en route: (1) event payload is typed `fiber.Map`, so handlers asserting `map[string]any` never fired — now assert `fiber.Map`; (2) events were emitted inside the request tx but async handlers read on a fresh connection, racing the commit — now emitted via `auth.AfterCommit` after `tx.Commit`.

### Phase 16 product reviews, live 2026-09-27 (commits `ce29b16` + `c6353e0`)

- **Deployed:** `ce29b16` (feature: `product_reviews` + `products.rating_average/rating_count`, `internal/reviews`, catalog rating fields, acceptance tests) then `c6353e0` (route-registration fix). Migration `0018` applied to prod DB manually via cross-compiled `migrate-linux` (`DATABASE_URL=postgres://shopkeet_app:shopkeet_app@10.0.1.10:5432/shopkeet` → `schema_migrations` = 18). Hosting rebuilds on every Coolify deploy, so the DB was migrated once, then the API was force-deployed (deploy `jm4uyrdefpi2r9v3qolf9nfi` → finished, container `...-150407835802` healthy).
- **Routing bug caught in live smoke:** `POST/GET /products/:id/reviews` were answered by a parseMerchant guard (403 "customer token not allowed here" / 401 "missing bearer token") instead of `CustomerAuthMW`/`PublicTenantMW`. Root cause: Fiber v2.52 applies a **group's middleware at request time by prefix**, so the catalog `/products` group's `TenantMW` leaked onto later-registered sibling routes. Unit tests couldn't catch it (they mount only reviews routes). **Fix: register reviews routes before catalog in `cmd/api/main.go`** (`c6353e0`); verified locally against the full route table, then re-deployed.
- **Live smoke passed end-to-end** against `https://api.shopkeet.com` (throwaway tenant `p16smoke-*`, cleaned after — no leftover):
  - Customer with a delivered order creates a review → `verified=true`; pending does *not* affect public count/aggregate.
  - Guards: rating 0 → 400, duplicate → 409, merchant create → 403, customer on `PATCH /reviews/:id` → 403.
  - Publish r1 → public count 1; publish unverified r2 (5★) → count 2, avg 4.5; `GET /products/:id` exposes `rating_average:4.5 rating_count:2`.
  - Reject r3 → aggregate unchanged; admin worklist `GET /reviews?status=rejected` returns exactly r3; `DELETE r2` → recompute to 4.0/1.
  - DB-level assertions via docker psql: `rating_count=1 rating_average=4.0`, 2 review rows, 1 verified+published.
- Rate-limit note: live smoke hit the `/auth/signup` and `/customers/signup` Redis buckets (10/hr/IP keyed on the VPS bridge IP `10.0.1.9`); cleared for the run with `redis-cli del "shopkeet:rl:*:10.0.1.9"`.

### Phase 17 abandoned-cart recovery, live 2026-09-27 (commit `46390e2`)

- **Deployed:** `46390e2` (feature: `carts.customer_email/last_activity_at/recovery_sent_at`, `POST /cart/email`, hourly Asynq `cart:abandonment` sweep, `cart_abandoned` notification type + sender, acceptance tests). Migration `0019` applied to prod DB via cross-compiled `migrate-linux` (`schema_migrations` = 19), then Coolify force-deploy `dwid6sl0plevyzhm6lbsqmbs` → finished, container `l6modsyezs1vlrv6ly1oqz4i-153417386013` healthy (image tag `46390e2d…`); logs confirm `notifications: using Resend provider`.
- **Live smoke passed** against `https://api.shopkeet.com` (throwaway tenant `p17smoke-*`, parked one kept for the hourly-job verification then cleaned — see below):
  - `POST /cart` (active product+variant) → 1 item; `POST /cart/email` echoes `cart.email`; bad email → 400.
  - Backdated `last_activity_at` 2h via DB → candidate scan (`customer_email NOT NULL`, `recovery_sent_at IS NULL`, `last_activity_at < now()-1h`, has items) finds **exactly 1** cart.
  - Stamping `recovery_sent_at` (the job's write) → scan re-run finds **0** — the once-only guarantee.
  - One smoke cart was left **parked** (idle 2h, email set, not stamped) while a detached VPS watcher waited for the next hourly tick to hit `notification_log` with `cart_abandoned|sent|<email>` + the `cart abandonment sweep emailed N carts` log line — confirming the real 60-minute path end-to-end. The tick at 16:38:41 UTC **did** fire ("emailed 1 carts", `recovery_sent_at` stamped on the parked cart, Resend `status=sent` for our smoke address) **but the `notification_log` row was misattributed** to tenant `00810252-...` (the first tenant in `ORDER BY id`) instead of `3d5571dd-...` — the sweep's recovery path leaned entirely on the RLS GUC (`app.current_tenant` via `set_config`) rather than the `tenant_id` column, and a scoped read under a pooled connection could resolve against the wrong tenant's scope. No customer data was exposed; the misattributed row was a phantom record under the wrong tenant.
- **Fix (`512cad0`, deployed 2026-09-28):** scope every recovery-path query explicitly by `tenant_id` so the sweep is correct-by-construction, independent of GUC/RLS mechanics:
  - Sweep candidates `WHERE tenant_id = $1 ...` (loop's `tid`), the mark `UPDATE ... WHERE id = $1 AND tenant_id = $2`, `SendCartAbandoned` email load `WHERE id = $1 AND tenant_id = $2`, item load `WHERE ci.cart_id = $1 AND ci.tenant_id = $2`.
  - Cross-tenant regression test in `cart_recovery_test.go`: two idle+email+item carts in different tenants, each swept under its **own** tenant id (spy filtered by tenant).
  - Full suite run as `shopkeet_app` (the FORCE-RLS owner role; the bootstrap test role is a superuser that bypasses RLS) — **all packages pass**.
- **Live re-verify after `512cad0` (fix deploy `rjdmdv0x7ayitoqass4hio9n`, container `l6modsyezs1vlrv6ly1oqz4i-020157878918`):** armed **two** candidates in two tenants (re-armed parked `3d5571dd` cart + a fresh tenant `c6ec6c18-...` cart), then enqueued `cart:abandonment` on-demand (`redis://shopkeet-redis:6379` inside the `coolify` docker network via a cross-compiled static enqueue binary) → sweep fired "emailed 2 carts"; **both `notification_log` rows landed under their own tenant** (`3d5571dd` → `3d5571dd`, `c6ec6c18` → `c6ec6c18`), both `status=sent`, both carts stamped. Deployed binary strings confirm the `AND tenant_id = $N` clauses are in the image.
- **Converted carts are structurally immune:** checkout deletes the cart + items in the order transaction (`orders.go` ~lines 408/412), so once an order exists the cart no longer exists to sweep. Covered by `TestCartRecoverySweep` (deletes the cart, sweep sends nothing).
- Cleanup after verification: all throwaway `p17smoke*` tenants removed (posts/sections/templates/redirects included — created at signup by Phase 6 content seeding and blocked tenant FKs on the first cleanup pass); only prod-facing data remains.

### Phase 18 gift cards, live 2026-09-28 (commits `a5a0dd3` + `19fb065`)
- **Deployed:** `a5a0dd3` (feature: `gift_cards` + `carts.gift_card_code` + `orders.gift_card_code`/`gift_card_cents`, `internal/giftcards`, `POST /cart/gift-card`, checkout claim, tests). Migration `0020` applied to prod DB via cross-compiled `migrate-linux` (`schema_migrations` = 20, clean; verified table + CHECKs + FORCE RLS + `OWNER shopkeet_app` + all 3 new columns). Coolify deploy `e0sdsfzqfk7g0t9ksfsfqo41` → finished, container `l6modsyezs1vlrv6ly1oqz4i-022536680437` healthy on image `a5a0dd3183d7…`.
- **Live smoke found a cross-tenant leak, then a deeper root cause.** Tenant B applied tenant A's `GC-PARTIAL` and got **200**. The gift-card lookup filtered on `code` only, relying on the RLS GUC to add the tenant.
- **Root cause of the root cause: production connected as a SUPERUSER.** Live env `DATABASE_URL = postgres://shopkeet:shopkeet@shopkeet-postgres:5432/shopkeet`, and `shopkeet` has `rolsuper = t`. Superusers **bypass `FORCE ROW LEVEL SECURITY` entirely**, so in production every `tenant_isolation` policy was inert and `set_config('app.current_tenant', …)` scoped nothing on its own. Confirmed directly: as `shopkeet` with the GUC pinned to another tenant, `SELECT … FROM orders` still returned **all 135** rows and 126 `merchant_users`. This is the same mechanism behind the Phase 17 misattribution, and it means the documented RLS guarantee had not been enforced in prod at all.
- **That is now fixed (deploy `15wz7ybxc4wgwvduovs5rvkd`): the live `DATABASE_URL` uses `shopkeet_app`.** Nothing had to move — same VPS, same `shopkeet-postgres` container, same `shopkeet` database, same host/port; only the role in the connection string changed. `shopkeet_app` already existed there (migration 0003), is `rolsuper = f`, owns all 28 RLS-protected tables **and every sequence**, and had already applied migration 0020. Pre-flight audit first, so the flip couldn't silently empty a table: every non-HTTP path sets the GUC itself (`internal/cart/cart.go:558` recovery sweep, `internal/notifications/notifications.go:183,269,332` background senders), `TenantMW`/`PublicTenantMW` set it per request (`internal/auth/auth.go:293`), and login resolves the tenant from the RLS-free `tenants` table *before* touching the protected `merchant_users` (`internal/auth/auth.go:180-206`).
  - The same query, three roles, one foreign GUC: `shopkeet_app` in its own tenant → 3 orders; `shopkeet_app` with a foreign GUC → **0**; superuser with a foreign GUC → **135**. RLS is now real, and `19fb065`'s explicit predicates are defence in depth behind it rather than the only barrier.
  - Full re-verify under enforced RLS: signup, merchant login, public catalog/categories/shipping on a **real** tenant, product + variant + zone + rate, discount, gift card, guest cart (2 × 2000), discount (400 off), gift card (1000), checkout `201`, admin orders/cards/discounts, media, tenant settings, customer signup/login, product review create + list. Cross-tenant probes all `404`/empty, and tenant B saw 0 of A's orders/cards/notifications from psql while the superuser saw all of them. Cleanup: all `p18*` smoke tenants removed, real data untouched (506 tenants, 135 orders, `schema_migrations|20`).
- **Two latent bugs the enforced-RLS smoke exposed, both pre-existing and both fixed:**
  - `f4ece4c` — `GET /notifications/log` had **always** answered 500. The handler type-asserted `c.Locals("tx")` against an inline interface, but `pgx.Tx.Query` returns the concrete `pgx.Rows` and Go requires *identical* method signatures, so the assertion could never succeed; every other handler in the codebase asserts `pgx.Tx` directly. Fixing that revealed a second fault hidden behind it: `sent_at` is `timestamptz` and was being scanned into a Go `string`, which pgx rejects. It now decodes into `time.Time` and formats RFC3339, the handler logs the real cause instead of swallowing it, and `TestListLogRoute` covers empty *and* populated states.
  - `3923cfd` — `POST /shipping/zones` answered 500 whenever `countries`/`regions` were omitted. Both columns are `TEXT[] NOT NULL DEFAULT '{}'`, but a Go `nil` slice is sent as an explicit NULL, which bypasses the default and trips the constraint. Every existing test happened to pass `regions` explicitly, which is why it survived. Omitted arrays now become empty arrays; `TestShippingRLSIsolation` gained a zone created with neither field.
- **Fix (`19fb065`): every tenant predicate is now explicit, so isolation holds by construction** regardless of DB role — the Phase 17 principle generalised:
  - `gift_cards`: `Resolve`/`Claim` take `tenantID` and use `WHERE tenant_id = $1 AND code = $2` (`+ FOR UPDATE`); the debit is `WHERE id = $2 AND tenant_id = $3`; `GET /gift-cards` lists `WHERE tenant_id = $1`.
  - `discounts` (same class, pre-existing since Phase 10): `Resolve`/`Claim` + `List`/`Get`/`Update`/`Delete` all carry `tenant_id`.
  - `carts`: `customer_session` is client-supplied and only unique per tenant (`UNIQUE (tenant_id, customer_session)`), so every cart query now filters `tenant_id` too — `loadCart`, item add/update/delete, discount apply, gift-card apply, `POST /cart/email`. **The checkout cart lookup was the worst case:** a guest could have named another tenant's `customer_session` and checked out that tenant's cart.
  - Regression test `TestTenantScopeWithoutRLS` connects as a **superuser** to reproduce the production condition (RLS would otherwise mask the bug) and asserts: other tenant's `Resolve`/`Claim` → 404, the owner's balance is not debited, and the owner still resolves normally.
- **Live re-verify after `19fb065`** (deploy `6wm4bohw4wdk96lmzmvaxpme` → finished, container `l6modsyezs1vlrv6ly1oqz4i-045123182894` healthy on image `19fb06515c01…`; binary strings confirm `FROM gift_cards WHERE tenant_id = $1 AND code = $2` and the carts/discounts predicates):
  - Guards: unknown code 404 · `amount_cents` 0/negative 400 · no bearer 401 · duplicate code 409 · lowercase `gc-full` normalised to `GC-FULL` · empty code auto-generates `GC-XXXXXXXX` · `GET /gift-cards` newest-first.
  - Partial card: 2000 + 500 shipping = 2500 due, `GC-PARTIAL` (1000) → order `gift_card_cents=1000`, `total_cents=1500`; card balance → 0; re-applying it then 400 `gift card has no remaining balance`.
  - Oversized card: 2500 due, `GC-FULL` (4000) → `gift_card_cents=2500`, **`total_cents=0`** (floor holds), **1500 left on the card**.
  - **Acceptance criterion live:** two concurrent checkouts racing for the last 500 of a 500 card → **exactly one 201 and one 400 `gift card has no remaining balance`**; card balance 0, exactly one order.
  - **Cross-tenant probe:** a second tenant applying either code → **404**; its cart carries no code; its `GET /gift-cards` → `[]`; DB confirms it owns 0 orders, 0 cards, 0 carts with a code.
  - Per-order snapshots verified in the DB (`GC-PARTIAL|1000|1500`, `GC-FULL|2500|0`, `GC-5B6DCEC0|500|10000`); no gift-card order exists outside the smoke tenants.
  - Cleanup: all 6 `p18*` smoke tenants removed (orders before tenants for the FK); `tenants|0`, `gift_cards|0`, no orphans. Real data untouched (506 tenants, 135 orders, `schema_migrations|20`).

### Phase 15 merchant operations, live 2026-09-27 (commit `0cf8112`)

- **Deployed:** `0cf8112` (merge `aa1bb6f..0cf8112 main -> main`), Coolify deploy `uf0oter0melzd2puvkk8zy4c` → finished, new container `l6modsyezs1vlrv6ly1oqz4i-090720660839` healthy. Migration `0017` applied to prod DB before the deploy (`schema_migrations` = 17).
- **Live smoke passed end-to-end** against `https://api.shopkeet.com` (throwaway tenant, then cleaned — zero leftover `p15*` subdomains):
  - `POST /orders/draft` (merchant JWT): `source=draft`, total `2900` (2×1000 + 20% tax + 500 shipping) with `tax_rate_percent=20` set; variant inventory 8 → 6.
  - `POST /orders/:id/returns` (merchant): `status=requested`, listed in `GET /returns`; `PATCH /returns/:id/status` `approved` → `received` restocked the **correct** variant (6 → 7); downgrade back to `requested` → **400**.
  - Customer-actor path: `POST /orders/:id/returns?phone=…` with `X-Tenant-ID` and **no** bearer → create `requested` (phone gate works); merchant `PATCH` to `received` with `restock=false` → inventory **stayed 7** (no incorrect restock).
  - DB assertions via `shopkeet-postgres`: `orders.source=draft`, `product_variants.inventory_count=7`, `returns.status=received`, `return_items` sum = 1.
- Smoke utility pattern: docker client over host network (`docker run --rm --network host postgres:16-alpine psql -h 10.0.1.10 …`) since the default docker bridge can't reach the `10.0.1.x` postgres bridge; cleanup must `SELECT set_config('app.current_tenant', $tid, true)` inside the tx or FORCE-RLS hides all tenant rows from `shopkeet_app`.

### Data layer (still manual containers, attached to `coolify` network)

- `shopkeet-postgres` (postgres:16-alpine) → volume `infra_postgres_data` — **the real DB**, migrations 0001–0020 (`schema_migrations` at version 20; 0016 idempotency keys, 0017 merchant operations = `orders.source` + `returns`/`return_items`, 0018 product reviews, 0019 abandoned-cart recovery columns, 0020 gift cards).
- `shopkeet-redis` (redis:7-alpine) → volume `infra_redis_data` — cart reservation + product cache-aside + rate limiting + Asynq queues (all verified live 2026-09-26: rate-limit key `shopkeet:rl:...` observed with 429s, cache key `shopkeet:cache:product:{tid}/{id}/public` observed + TTL'd).
- App connects via hostname **`shopkeet-postgres`** / **`shopkeet-redis`**. ⚠️ Do NOT use host `postgres` — or `redis` — on the coolify network: `coolify-db`/`coolify-redis` own those aliases and they point at Coolify's own auth'd instances (the `NOAUTH` incident on 2026-09-26).
- Old manual `shopkeet-api` compose container: **stopped and removed** (Coolify is now the only API).
- Healthcheck note: runtime image includes `curl`; app binds IPv4 only, so Coolify's in-container check (localhost → `::1`) relies on curl's fallback to `127.0.0.1`.

### Cleanup performed 2026-09-25 (VPS + Coolify)

- Deleted Coolify apps: old `hkgdwooa0vivixdehdqpxwfr`, junk `s28ucomlzbvv09w1mcsif8ym`, `yfhv2ed0ywnayqascwr0eoa`.
- Deleted unused dev DBs `pilot-pg` (`tyjcf7e0bdoi4bnuskwxkfmi`) and `pilot-redis` (`cocsvhri8k91dcvp1n6gepds`) — leftovers, not referenced by the app.
- Removed containers `manual-hc` (debug), old `shopkeet-api`, `shopkeet-caddy` (never used).
- Removed images `shopkeet-api:latest`, `caddy:2.8-alpine`, stale `l6mods…:66b01a4` build.
- Removed volumes `caddy_config`, `caddy_data`, `infra_caddy_*`, `infra_grafana_data`, 2 anonymous Prometheus/empty volumes.
- Final volume set: `coolify-db`, `coolify-redis`, `infra_postgres_data`, `infra_redis_data`. Disk 38G (8.8G used, 29G free).

### Known good paths (for verification after any change)

- `curl https://api.shopkeet.com/healthz` → 200.
- `POST https://api.shopkeet.com/api/v1/auth/signup` `{name, subdomain, email, password}` → 200 + JWT. (Payload field is **`subdomain`**, not `tenant_name`.)
- Signups appear in `shopkeet-postgres`/`shopkeet` DB (`tenants`), never in coolify-db.
- `POST /auth/login`, public `GET /products`, admin `/products` with JWT beside `X-Tenant-ID` all pass.
- **Notifications E2E:** `POST /customers/signup` `{email, password}` with `X-Tenant-ID: <tenant uuid>` → `customer_welcome`; guest checkout s→`order_confirmation`; `PATCH /orders/:id/status` pending→confirmed→shipped→delivered → `order_delivered`. Watch `GET /notifications/log` (rows `sent`) — mails go out via **Resend** (dashboard inbox, `mail.shopkeet.com`).

**Gotchas:**
- `X-Tenant-ID` must be the tenant **UUID**, not the subdomain — `PublicTenantMW` does `set_config('app.current_tenant', <header>)` and RLS casts `::uuid`, so a subdomain → `500 internal_error`.
- Merchant signup does **not** emit `customers.signup`; only customer account signup does.
- Guest cart/checkout rides `X-Customer-Session` header (e.g. `sess-e2e-1`).
- Order status transitions are strictly linear: `pending → confirmed → shipped → delivered` (or cancel from pending/confirmed); jumping straight to `delivered` → `400 invalid status transition`.
- Coolify auto-deploy webhook has not been observed firing; after a push, force deploy via the **verified** endpoint: `POST /api/v1/deploy?uuid=l6modsyezs1vlrv6ly1oqz4i&force=true` (NOT `/applications/{uuid}/start` or `/applications/{uuid}/deploy` — both 404).
- ⚠️ **Never rely on the RLS GUC to scope a query in this codebase.** Every tenant predicate must be explicit (`WHERE tenant_id = $N`) — three live cross-tenant bugs came from that assumption (Phase 17 recovery, Phase 18 gift cards, Phase 18 cart/discounts). As of 2026-09-28 the live role is `shopkeet_app`, so RLS is a real second layer, but a missing predicate that relied on RLS would now show up as **empty results** rather than a leak, which is the correct failure direction. Corollary for ad-hoc prod debugging: `psql` as `shopkeet` + `set_config(...)` still shows *all* tenants' rows (superuser bypass), so DB verification must filter `tenant_id` explicitly, and use `-U shopkeet_app` when you want to see what the app actually sees.

### Migration runbook

See `SHOPKEET-COOLIFY-MIGRATION.md` → **"Executed: API-driven deployment"** for the exact API endpoints, payloads, and gotchas (PowerShell BOM, base64-over-SSH pattern, `postgres` alias collision, curl-in-alpine).

---

## Migration Status (VPS)

| Version | Phase | Applied | Type |
|---------|-------|---------|------|
| 1 | 0 | ✓ | Scaffolding |
| 2 | 1 | ✓ | Tenants + Auth |
| 3 | 1 | ✓ | App role `shopkeet_app` |
| 4 | 1 | ✓ | Default grants |
| 5 | 2 | ✓ | Media |
| 6 | 3 | ✓ | Catalog |
| 7 | 4 | ✓ | Cart |
| 8 | 5 | ✓ | Orders |
| 9 | 6 | ✓ | Content |
| 10 | 8 | ✓ | Variants (superuser DML backfill) |
| 11 | 9 | ✓ | Shipping |
| 12 | 10 | ✓ | Discounts |
| 13 | 11 | ✓ | Customers |
| 14 | 12 | ✓ | Notifications |
| 15 | 13 | ✓ | Settings + Tax + Notes |
| 16 | 14 | ✓ | Idempotency |
| 17 | 15 | ✓ | Merchant Operations: `orders.source` + `returns` + `return_items` |
| 18 | 16 | ✓ | Product Reviews: `product_reviews` + `products.rating_average/rating_count` |
| 19 | 17 | ✓ | Abandoned Cart Recovery: `carts.customer_email` + `last_activity_at` + `recovery_sent_at` (+ partial scan index) |
| 20 | 18 | ✓ | Gift Cards: `gift_cards` + `carts.gift_card_code` + `orders.gift_card_code`/`gift_card_cents` |
| 21 | 19 | ✓ | Pre-orders & Back-in-Stock: `product_variants.allow_preorder`/`preorder_ships_at`, `order_items.is_preorder`, `back_in_stock_subscriptions` + `notification_log` type `back_in_stock` |
| 22 | 20 | ✓ | Loyalty & Referrals: `loyalty_ledger` + `customers.loyalty_points`/`discounts.customer_id` + tenant loyalty settings |
| 23 | 21 | ✓ | Advanced & Automatic Discounts: `discounts.applies_to`/`requires_code`/`buy_quantity`/`get_quantity` |
| 24 | 22 | ✓ | Wishlist: `wishlist_items` |
| 25 | 23 | ✓ | Order Tracking + Invoice PDF: `orders.tracking_number`/`tracking_carrier`/`tracking_url` |
| 26 | 24 | ✓ | Storefront Analytics: indexes `orders_created_at_idx` (`orders(tenant_id, created_at DESC)`), `order_items_order_idx` (`order_items(order_id)`), `carts_created_at_idx` (`carts(tenant_id, created_at)`) — no new tables |
| 27 | 25 | ✓ | Product Bundles & Quantity Breaks: `bundles` + `bundle_items` + `quantity_breaks` + `cart_items.bundle_id`/`order_items.bundle_id` |
| 28 | 26 | x | Upsell, Cross-sell & Post-Purchase: `product_recommendations` (manual/auto, per-pick unique) + post-purchase upsell keyed on `orders.customer_phone` |

---

## Acceptance Tests (Integration, run against VPS DB)

| Test | Package | Phase |
|------|---------|-------|
| `TestTenantRLSIsolation` | `internal/auth` | 1 |
| `TestMediaRLSIsolation` | `internal/media` | 2 |
| `TestCatalogRLSIsolation` | `internal/catalog` | 3 |
| `TestCartRLSIsolation` | `internal/cart` | 4 |
| `TestRedisReserver` | `internal/cart` | 4 |
| `TestOrdersRLSIsolation` | `internal/orders` | 5, 9, 10, 13 |
| `TestContentRLSIsolation` | `internal/content` | 6 |
| `TestErrorShape` | `internal/platform` | 7 |
| `TestMetricsExposition` | `internal/platform` | 7 |
| `TestVariantsRLSIsolation` | `internal/catalog` | 8 |
| `TestShippingRLSIsolation` | `internal/shipping` | 9 |
| `TestDiscountsAcceptance` | `internal/discounts` | 10 |
| `TestCustomersRLSIsolation` | `internal/customers` | 11 |
| `TestIdempotencyReplay` | `internal/platform/idempotency` | 14 |
| `TestIdempotencyPurgeExpired` | `internal/platform/idempotency` | 14 |
| `TestRateLimitWindow` | `internal/platform/ratelimit` | 14 |
| `TestRedisSetGetRoundTrip`, `TestRedisDelPrefix`, `TestInvalidateProduct` | `internal/platform/cache` | 14 |
| `TestProductCacheAside` | `internal/catalog` | 14 |
| `TestDraftOrderDecrementsStock` | `internal/orders` | 15 |
| `TestReturnRestocksCorrectVariant` | `internal/orders` | 15 |
| `TestReviewLifecycleAndVerified` | `internal/reviews` | 16 |
| `TestRejectAndDeleteRecompute` | `internal/reviews` | 16 |
| `TestCartRecoverySweep` | `internal/cart` | 17 |
| `TestSendCartAbandoned` | `internal/notifications` | 17 |
| `TestGiftCardsAdmin` | `internal/giftcards` | 18 |
| `TestResolveValidations` | `internal/giftcards` | 18 |
| `TestTenantScopeWithoutRLS` | `internal/giftcards` | 18 |
| `TestGiftCardCheckoutSnapshot` | `internal/orders` | 18 |
| `TestGiftCardConcurrentDoubleSpend` | `internal/orders` | 18 |
| `TestPreorderCheckout` | `internal/orders` | 19 |
| `TestNotifyMe` | `internal/catalog` | 19 |
| `TestVariantPreorderFields` | `internal/catalog` | 19 |
| `TestDeliverBackInStockExactlyOnce` | `internal/notifications` | 19 |
| `TestLoyaltyReferrals` | `internal/loyalty` | 20 |
| `TestAutomaticDiscounts` | `internal/discounts` | 21 |
| `TestWishlistFlow` | `internal/wishlist` | 22 |
| `TestOrderTrackingAndInvoicePDF` | `internal/orders` | 23 |
| `TestInvoiceTotalsMath` | `internal/orders` | 23 |
| `TestAnalyticsReconciliation` | `internal/analytics` | 24 |
| `TestParsePeriodAndLimit` | `internal/analytics` | 24 |
| `TestBundlesAcceptance` | `internal/bundles` | 25 |
| `TestRecommendationsAcceptance` | `internal/recommendations` | 26 |

Run:  
```bash
DATABASE_URL="postgres://shopkeet_app:shopkeet_app@localhost:5432/shopkeet?sslmode=disable" \
REDIS_URL="redis://localhost:6379" \
go test -count=1 ./...
```

---

## Deferred / Not Yet Built

- Redis read-through cache for shipping rates (product/catalog cache-aside ships in Phase 14)
- Webhooks / developer marketplace (`/webhooks/*`)
- Public GraphQL/REST developer API
- Online payments beyond COD
- Post/template revision history
- Blog archive, search-results, announcement-bar templates
- Analytics dashboard
- Granular staff permissions beyond `owner`/`staff`
- Refund tracking
- Multi-jurisdiction tax engine