# Shopkeet API — Complete Reference (Phases 1–13)

**Last updated:** 2026-09-25  
**DB version:** 15 (migrations 0001–0015 applied on VPS)  
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
14. [Database Schema Summary](#database-schema-summary)
15. [Auth Scopes & Middleware](#auth-scopes--middleware)
16. [Error Shape](#error-shape)
17. [Env Vars & Config](#env-vars--config)

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
- `ResendProvider` — HTTPS POST to `api.resend.com` (requires `RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL`)
- `SMTPProvider` — Go stdlib `net/smtp`; the live path in production

### Provider resolution (`main.go`)

Priority: **SMTP → Resend → Log**. SMTP wins the moment `SMTP_HOST` is set (even non-empty). Resend needs `RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL`. Otherwise LogProvider.

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
```

**Every tenant-scoped table has:**
- `tenant_id UUID NOT NULL REFERENCES tenants(id)`
- `ENABLE ROW LEVEL SECURITY`
- `FORCE ROW LEVEL SECURITY`
- `CREATE POLICY tenant_isolation USING (tenant_id = current_setting('app.current_tenant', true)::uuid)`
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

## Env Vars & Config

| Variable | Required | Phase | Description |
|----------|----------|-------|-------------|
| `DATABASE_URL` | Yes | 0 | Postgres as `shopkeet_app` (non-superuser) |
| `REDIS_URL` | No | 4 | Cart reservation (falls back to NoopReserver) |
| `JWT_SECRET` | Yes | 1 | HS256 signing key |
| `PORT` | No (3001) | 0 | HTTP port |
| `APP_BASE_DOMAIN` | No | 1 | Tenant subdomain base (e.g. `shopkeet.com`) |
| `METRICS_TOKEN` | No | 7 | Bearer token for `/metrics` |
| `R2_ACCOUNT_ID` | Set together | 2 | Cloudflare R2 credentials |
| `R2_ACCESS_KEY_ID` | Set together | 2 | |
| `R2_SECRET_ACCESS_KEY` | Set together | 2 | |
| `R2_BUCKET_NAME` | Set together | 2 | |
| `R2_PUBLIC_URL` | Set together | 2 | Public base for media URLs |
| `RESEND_API_KEY` | No | 12 | Transactional email (Resend) — used only when SMTP is NOT set |
| `NOTIFICATIONS_FROM_EMAIL` | No | 12 | From address for notifications (SMTP envelope + Resend sender) |
| `SMTP_HOST` | No | 12 | SMTP server host — **set this to enable SMTP provider** (overrides Resend) |
| `SMTP_PORT` | No | 12 | SMTP port (default `587`) — `1025` for Mailpit |
| `SMTP_TLS_MODE` | No | 12 | `starttls` (default) \| `tls` \| `none` |
| `SMTP_TLS_VERIFY` | No | 12 | `true` (default) verify cert; `false` for Mailpit/self-signed |

---

## Live Deployment Status (VPS 13.61.125.59) — 2026-09-25

### Running under Coolify (single source of truth for the API)

> **Deploy decision (re-confirmed 2026-09-25):** Coolify is the deploy layer — the frontend will run on it too, so there's no Vercel and one management surface. This reverses the original compose+Caddy-for-RAM preference (see `shopkeet-agents-package/.agents/rules/backend-constraints.md` and `docs/03-architecture.md` §7). Caddy and its on-demand-TLS redesign for merchant custom domains is **deferred**, not dropped.

| Resource | Identifier | Status |
|----------|-----------|--------|
| Coolify app `shopkeet-api` | uuid `l6modsyezs1vlrv6ly1oqz4i` | **running:healthy** |
| Live commit | `1368afb` (`main`) | container `l6modsyezs1vlrv6ly1oqz4i-152514994209`, image `:1ce5bd40…` |
| Domain | `https://api.shopkeet.com` | 200 (`/healthz` → `{"status":"ok"}`), TLS via Coolify proxy |
| Source | `Theusama1183/shopkeet-backend`, branch `main` | build pack `dockerfile`, `base_directory /apps/api`, `dockerfile_location /Dockerfile`, `ports_exposes 3001` |
| Auto-deploy | `is_auto_deploy_enabled=true` | pushes to `main` trigger builds (webhook; fallback: `POST /api/v1/applications/{uuid}/start`) |
| Env | 15 vars incl. `DATABASE_URL`, `REDIS_URL`, JWT/R2/METRICS + `APP_BASE_DOMAIN=shopkeet.com` + `SMTP_*` | `PORT` unset → default 3001 |

### Notifications & email (Phase 12, live 2026-09-25)

- Provider resolution in `main.go`: **SMTP** (`SMTP_HOST` ≥ 1 var) → **Resend** (`RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL`) → **LogProvider**.
- ⚠️ **Mailpit is a dev/staging mail *catcher*, not a relaying provider.** It acknowledges the message and shows it in its UI but never delivers to real inboxes. `notification_log` rows say `sent` because SMTP accepted — the message is NOT on the road to the customer. **Before any real merchant ships, replace it with a real provider:** set `RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL` (Resend v2 free tier ≈ 3000 emails/mo) or point `SMTP_HOST*` at a relaying server (SES/Brevo/Postmark) and add SPF/DKIM on the sending domain. The `Provider` interface is the swap seam — no code change needed.
- Current SMTP → **Mailpit** service in Coolify (SMTP `:1025`, no auth): sees the app container as `mailpit-p1zaxdgvrrdf9p6bb1czudqi`; from the VPS host use `127.0.0.1:1025`. Set env vars: `SMTP_HOST`, `SMTP_PORT=1025`, `SMTP_TLS_MODE=starttls` (falls back to plaintext when the peer doesn't advertise STARTTLS), `SMTP_TLS_VERIFY=false`, `NOTIFICATIONS_FROM_EMAIL=no-reply@shopkeet.com`.
- Mailpit UI (HTTPS, LE cert via Traefik): `https://mailpit-p1zaxdgvrrdf9p6bb1czudqi.13.61.125.59.sslip.io`. Messages API: `GET /api/v1/messages?limit=N`.
- Shopify-style addressing (per store): `From: "<StoreName> via Shopkeet <no-reply@<subdomain>.shopkeet.com>"`, `Reply-To: `support@<subdomain>.shopkeet.com`` (base domain from `APP_BASE_DOMAIN`). Verified live in Mailpit headers.
- E2E verified against Mailpit: `customers.signup` → `customer_welcome`, checkout → `order_confirmation`, status → `delivered` → `order_delivered`; all `notification_log` rows `status=sent`.
- Two latent bugs fixed en route: (1) event payload is typed `fiber.Map`, so handlers asserting `map[string]any` never fired — now assert `fiber.Map`; (2) events were emitted inside the request tx but async handlers read on a fresh connection, racing the commit — now emitted via `auth.AfterCommit` after `tx.Commit`.

### Data layer (still manual containers, attached to `coolify` network)

- `shopkeet-postgres` (postgres:16-alpine) → volume `infra_postgres_data` — **the real DB**, migrations 0001–0015.
- `shopkeet-redis` (redis:7-alpine) → volume `infra_redis_data` — cart reservation/units.
- App connects via hostname **`shopkeet-postgres`** / **`shopkeet-redis`**. ⚠️ Do NOT use host `postgres` on the coolify network — `coolify-db` owns that alias and it points at Coolify's own DB.
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
- **Notifications E2E:** `POST /customers/signup` `{email, password}` with `X-Tenant-ID: <tenant uuid>` → `customer_welcome`; guest checkout s→`order_confirmation`; `PATCH /orders/:id/status` pending→confirmed→shipped→delivered → `order_delivered`. Watch `GET /notifications/log` (rows `sent`) and Mailpit UI for the mails.

**Gotchas:**
- `X-Tenant-ID` must be the tenant **UUID**, not the subdomain — `PublicTenantMW` does `set_config('app.current_tenant', <header>)` and RLS casts `::uuid`, so a subdomain → `500 internal_error`.
- Merchant signup does **not** emit `customers.signup`; only customer account signup does.
- Guest cart/checkout rides `X-Customer-Session` header (e.g. `sess-e2e-1`).
- Order status transitions are strictly linear: `pending → confirmed → shipped → delivered` (or cancel from pending/confirmed); jumping straight to `delivered` → `400 invalid status transition`.
- Coolify auto-deploy webhook has not been observed firing; after a push, force deploy: `POST /api/v1/applications/l6modsyezs1vlrv6ly1oqz4i/start?force=true`.

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

Run:  
```bash
DATABASE_URL="postgres://shopkeet_app:shopkeet_app@localhost:5432/shopkeet?sslmode=disable" \
REDIS_URL="redis://localhost:6379" \
go test -count=1 ./...
```

---

## Deferred / Not Yet Built

- Redis read-through cache for hot public reads (catalog, shipping rates)
- Webhooks / developer marketplace (`/webhooks/*`)
- Public GraphQL/REST developer API
- Online payments beyond COD
- Post/template revision history
- Blog archive, search-results, announcement-bar templates
- Reviews, wishlists, abandoned-cart recovery
- Analytics dashboard
- Granular staff permissions beyond `owner`/`staff`
- Refund tracking
- Multi-jurisdiction tax engine