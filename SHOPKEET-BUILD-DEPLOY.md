# Shopkeet — Build & Deployment Report

Everything the backend shipped (Phase 0 → Phase 7), every endpoint, every table with every field, plus the current live deployment state and the domain-handoff checklist for connecting DNS/TLS.

Status: **v1 backend COMPLETE and DEPLOYED** (Phases 0–7; last commit `777f269`).

---

## 1. Live deployment (current state)

| Item | Value |
|---|---|
| VPS | `13.61.125.59` (Ubuntu 26.04, single VPS) |
| Deploy method | plain `docker compose` on the VPS (Coolify/Traefik not installed) |
| Stack root | `/home/ubuntu/shopkeet/` (`apps/api` source, `infra/` compose + monitoring config) |
| Compose project | `infra` |

### Containers

| Container | Image | Port (host) | Health |
|---|---|---|---|
| `shopkeet-postgres` | postgres:16-alpine | `127.0.0.1:5432` | healthy, data persisted (migrations up to `0009`) |
| `shopkeet-redis` | redis:7-alpine | `127.0.0.1:6379` | healthy |
| `shopkeet-api` | built from `apps/api/Dockerfile.prod` (static, non-root `shopkeet`) | `127.0.0.1:3001` | healthy (`/healthz` → 200) |
| `shopkeet-prometheus` | prom/prometheus:v2.53.0 | `127.0.0.1:9090` | target `api:3001` UP |
| `shopkeet-grafana` | grafana/grafana:11.1.0 | `127.0.0.1:3000` | 200 (dashboard "Shopkeet API" provisioned) |

All ports are bound to **localhost on the VPS only** — nothing is publicly reachable yet. `DATABASE_URL`, `REDIS_URL` resolve inside the compose network (`postgres`, `redis`).

### Deploy secrets (`/home/ubuntu/shopkeet/infra/api.env`)

- `JWT_SECRET` — random 48-hex string
- `APP_BASE_DOMAIN` — currently `shopkeet.dev` (placeholder; update once the real domain is attached)
- `METRICS_TOKEN` — random 24-hex string; required as `Authorization: Bearer <token>` on `/metrics` (used by Prometheus)

### Verified live (smoke test, 2026-09-24)

- `POST /api/v1/auth/signup` → 200, tenant `deploytest` created, owner JWT returned
- `GET /api/v1/posts?post_type=page&route=/` with `X-Tenant-ID` → seeded **Home** page post (signup seeding hook works in prod)
- `GET /metrics` with bearer token → `shopkeet_http_requests_total`, latency histogram, and `shopkeet_db_pool_{max,acquired,idle,total}` metrics present
- Prometheus `up{instance="api:3001"} = 1`; Grafana `/api/health` → 200

### Not configured on the VPS yet

- **Cloudflare R2 (media)**: not set → `/media/*` routes are NOT mounted. Add `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_BUCKET_NAME`, `R2_PUBLIC_URL` to `api.env` and recreate the api container to enable.
- **Domain + TLS**: no public exposure yet. See §Domain/DNS checklist.

---

## 2. Phase-by-phase build log

Stack: Go (module `github.com/shopkeet/api`) + Fiber v2 + pgx v5 + Redis (go-redis v9) + Prometheus/Grafana. One modular monolith. Every tenant-scoped table: `tenant_id` + `FORCE ROW LEVEL SECURITY` policy defined **in the same migration**, owner `shopkeet_app` (a dedicated non-superuser role the API connects as — the `shopkeet` superuser is only for migrations/admin). Migrations are golang-migrate, embedded in the binary (`cmd/migrate`).

### Phase 0 — Scaffolding (commit `eaefb7f`…)
Go service + Fiber + pgx pool + embedded golang-migrate + `/healthz`.

### Phase 1 — Tenants & Auth (`0001_init`, `0002_tenants_auth`, `0003_app_role`, `0004_default_grants`)

**Endpoints**
| Method | Path | Auth |
|---|---|---|
| POST | `/api/v1/auth/signup` | Public — creates tenant + owner in one tx, returns JWT |
| POST | `/api/v1/auth/login` | Public — returns JWT (`tenant_id`, `user_id`, `role`) |

**Tables & fields**
- `tenants` (6 fields): `id UUID PK`, `name TEXT`, `subdomain TEXT UNIQUE`, `custom_domain TEXT UNIQUE`, `status TEXT (active/suspended)`, `created_at TIMESTAMPTZ`. Not RLS-scoped (root table).
- `merchant_users` (6): `id`, `tenant_id → tenants`, `email`, `password_hash`, `role TEXT (owner/staff)`, `created_at`. `UNIQUE (tenant_id, email)`. RLS FORCE.

Notes: JWT middleware `auth.TenantMW` sets `SET LOCAL app.current_tenant` per request → RLS scopes every query. `shopkeet_app` role owns tenant tables so RLS can't be bypassed by the owner.

### Phase 2 — Media Library / Cloudflare R2 (`0005_media_assets`)
File bytes never pass through the API — client uploads straight to R2 via a short-lived presigned URL.

**Endpoints**
| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/api/v1/media/upload-url` | Admin | presigned R2 PUT URL → `{upload_url, r2_key, expires_in}` |
| POST | `/api/v1/media` | Admin | confirm upload, record asset, return public `url` |
| GET | `/api/v1/media` | Admin | list tenant's library |
| DELETE | `/api/v1/media/:id` | Admin | delete from R2 + DB |

**Table**: `media_assets` (8): `id`, `tenant_id`, `r2_key TEXT UNIQUE` (`{tenant_id}/{uuid}.{ext}`), `url`, `content_type`, `size_bytes INT`, `alt_text`, `created_at`. RLS FORCE. Stolen-key (foreign tenant id prefix) → 403 as defense-in-depth.

### Phase 3 — Catalog (`0006_catalog`)

**Endpoints**
| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/api/v1/products?search=&category=` | Public | `status=active` only; tsvector search + category-slug filter |
| GET | `/api/v1/products/:id` | Public(active)/Admin | both views; embeds ordered images + categories |
| POST | `/api/v1/products` | Admin | create |
| PATCH | `/api/v1/products/:id` | Admin | update |
| DELETE | `/api/v1/products/:id` | Admin | delete |
| POST | `/api/v1/products/:id/images` | Admin | attach `media_asset_id` + `sort_order` |
| DELETE | `/api/v1/products/:id/images/:imageId` | Admin | detach |
| GET | `/api/v1/categories` | Public | list |

**Tables & fields**
- `categories` (4): `id`, `tenant_id`, `name`, `slug`. `UNIQUE (tenant_id, slug)`. RLS FORCE.
- `products` (13: 12 explicit + generated `search_vector`): `id`, `tenant_id`, `name`, `slug`, `description`, `price_cents INT`, `currency TEXT default 'usd'`, `inventory_count INT`, `status TEXT (draft/active/archived)`, `meta_title`, `meta_description`, `search_vector TSVECTOR GENERATED` (name+description, GIN index), `created_at`. `UNIQUE (tenant_id, slug)`. RLS FORCE.
- `product_categories` (3): `tenant_id`, `product_id`, `category_id`, PK `(product_id, category_id)`. RLS FORCE.
- `product_images` (5): `id`, `tenant_id`, `product_id`, `media_asset_id`, `sort_order`, `UNIQUE (product_id, media_asset_id)`. RLS FORCE.

Gotcha handled: unique-violation aborts the DB tx → conflict-prone writes run under a `SAVEPOINT` so a predicted 4xx keeps the tx usable.

### Phase 4 — Cart & Inventory (`0007_cart`)
Guest cart, no account. Session key `customer_session` rides in `shopkeet_session` cookie or `X-Customer-Session` header.

**Endpoints**
| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/api/v1/cart` | Customer | fetch (null for fresh session) |
| POST | `/api/v1/cart` | Customer | create / add item (merges same product qty) |
| PATCH | `/api/v1/cart/items/:id` | Customer | change quantity |
| DELETE | `/api/v1/cart/items/:id` | Customer | remove |

**Tables & fields**
- `carts` (4): `id`, `tenant_id`, `customer_session TEXT`, `created_at`. `UNIQUE (tenant_id, customer_session)`. RLS FORCE.
- `cart_items` (5): `id`, `tenant_id`, `cart_id`, `product_id`, `quantity INT CHECK (>0)`, `UNIQUE (cart_id, product_id)`. RLS FORCE.

Add-to-cart is **not** a stock gate (matches spec) — only `status=active` products are cartable. Optional best-effort Redis reservation (`SETNX shopkeet:reserve:{product_id}:{unit_index}`, TTL 15 min) via `cart.Reserver` (`NoopReserver` if `REDIS_URL` unset). Socket arbitration is authoritative at checkout.

### Phase 5 — Checkout & Orders, COD (`0008_orders`)

**Endpoints**
| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/api/v1/checkout` | Customer | validate stock, `payment_method=cod`, decrement inventory, clear cart, emit `order.created` |
| GET | `/api/v1/orders/:id?phone=&email=` | Customer | lookup keyed on phone |
| GET | `/api/v1/orders?status=` | Admin | list + filter |
| PATCH | `/api/v1/orders/:id/status` | Admin | `pending→confirmed→shipped→delivered`; delivered sets `payment_status=paid` + emits `order.paid`; `pending/confirmed→cancelled` |

**Tables & fields**
- `orders` (12): `id`, `tenant_id`, `customer_name`, `customer_phone`, `customer_email`, `shipping_address`, `payment_method DEFAULT 'cod'`, `payment_status (pending/paid/failed)`, `status (pending/confirmed/shipped/delivered/cancelled)`, `total_cents INT`, `currency DEFAULT 'usd'`, `created_at`. RLS FORCE.
- `order_items` (6): `id`, `tenant_id`, `order_id`, `product_id`, `quantity`, `unit_price_cents INT` (price snapshot — survives edits). RLS FORCE.

Anti-oversell: checkout locks every carted product `SELECT … FOR UPDATE`; two concurrent checkouts for the last unit serialize, second → 409. In-process event bus `events.Bus` emits `order.created` / `order.paid` (main.go subscribes a log handler — seam for future webhooks). Payments behind `payments.Provider` interface; v1 ships only `CODProvider`.

### Phase 6 — Content & Page Builder (`0009_content`)
Backend half (Puck editor UI + storefront rendering are the future `apps/web`). Layouts are Puck-compatible JSON stored **verbatim**.

**Endpoints**
| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/api/v1/posts?post_type=page&route=/` | Public | one published post by type + route |
| GET | `/api/v1/posts?post_type=blog_post` | Public | list published posts of a type |
| POST | `/api/v1/posts` | Admin | create post |
| PATCH | `/api/v1/posts/:id` | Admin | update post (layout/SEO/status); route change auto-creates a redirect |
| DELETE | `/api/v1/posts/:id` | Admin | delete |
| GET | `/api/v1/templates/:template_type` | Public | fetch `scope=default` published template |
| PUT | `/api/v1/templates/:template_type` | Admin | upsert template (Puck save point) |
| GET | `/api/v1/sections?section_type=header` | Public | published chrome (header/footer) |
| GET | `/api/v1/sections?section_type=popup` | Public | active popups (client evaluates `placement_rules`) |
| POST | `/api/v1/sections` | Admin | create section |
| PATCH | `/api/v1/sections/:id` | Admin | update layout / rules / status |
| DELETE | `/api/v1/sections/:id` | Admin | delete |
| GET | `/api/v1/redirects` | Admin | list |
| POST | `/api/v1/redirects` | Admin | create |
| DELETE | `/api/v1/redirects/:id` | Admin | delete |
| GET | `/api/v1/redirects/lookup?path=` | Public | storefront 404-fallback lookup |

**Tables & fields**
- `posts` (12): `id`, `tenant_id`, `post_type DEFAULT 'page'`, `route`, `title`, `layout JSONB`, `meta_title`, `meta_description`, `og_image_id → media_assets`, `status (draft/published)`, `published_at`, `updated_at`. `UNIQUE (tenant_id, route)`. RLS FORCE.
- `templates` (9): `id`, `tenant_id`, `template_type`, `scope DEFAULT 'default'`, `layout JSONB`, `meta_title`, `meta_description`, `status`, `updated_at`. `UNIQUE (tenant_id, template_type, scope)`. RLS FORCE.
- `sections` (8): `id`, `tenant_id`, `section_type (header/footer/announcement_bar/popup)`, `name`, `layout JSONB`, `placement_rules JSONB`, `status`, `updated_at`. RLS FORCE.
- `redirects` (5): `id`, `tenant_id`, `from_path`, `to_path`, `created_at`. `UNIQUE (tenant_id, from_path)`. RLS FORCE.

**Signup seeding**: `auth.RegisterTenantCreatedHook` → `content.SeedDefaults` runs inside the signup tx — new tenants get the Home `page` post at `/`, the 5 templates (`product`, `product_archive`, `cart`, `404`, `order_confirmation`), and `header`/`footer` sections, all published. Storefront works out of the box.

### Phase 7 — Observability & Hardening (commit `777f269` — last phase)

**Endpoints**
| Method | Path | Auth |
|---|---|---|
| GET | `/healthz` | Public |
| GET | `/metrics` | Internal — `Authorization: Bearer <METRICS_TOKEN>` when set |

**What shipped**
- **Standard error shape** (every endpoint): `{"error":{"code":"...","message":"..."}}`. Codes: `invalid_request`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `upstream_error`, `internal_error` (generic), plus stable resource codes like `product_not_found`. Panic → recovered → `internal_error`, no stack trace. Implemented by `internal/platform/httperr` (single Fiber `ErrorHandler`); all 92 legacy inline error responses across auth/media/catalog/cart/orders/content migrated.
- **Structured logging**: `internal/platform/observe` — `RequestID` (mints/echoes `X-Request-ID`), `AccessLog` (one structured JSON line/request: request_id, method, path, status, latency_ms, tenant_id, role). Mount order: recover → RequestID → AccessLog → Metrics.
- **Prometheus metrics**:
  - `shopkeet_http_requests_total{method,route,status}` (route = matched Fiber pattern)
  - `shopkeet_http_request_duration_seconds{method,route}` (histogram)
  - `shopkeet_db_pool_{max,acquired,idle,total}` (gauges, sampled per scrape)
- **Grafana**: provisioned datasource (Prometheus) + "Shopkeet API" dashboard (RPS by route, status distribution, p50/p95/p99 latency, 4xx/5xx by route, DB pool). Config lives in `infra/prometheus/prometheus.yml` + `infra/grafana/provisioning/`.

---

## 3. Total endpoint/table inventory (v1)

- **Endpoints: 40** (2 auth + 4 media + 8 catalog + 4 cart + 4 checkout/orders + 16 content + 2 platform)
- **Tables: 15** (tenants, merchant_users, media_assets, categories, products, product_categories, product_images, carts, cart_items, orders, order_items, posts, templates, sections, redirects)
- All tenant-scoped (14 of 15) are `FORCE ROW LEVEL SECURITY` + owner `shopkeet_app`.

---

## 4. Domain/DNS + TLS checklist (for Claude / domain owner)

These steps are **not done yet** — the API is currently localhost-only on the VPS.

1. **Pick the domain** (e.g. `shopkeet.com`). Decide the API subdomain: `api.<domain>` per `docs/03-architecture.md`.
2. **Cloudflare DNS**: point the domain's nameservers to Cloudflare (if not already). Add an **A record** for the API host, e.g. `api` → `13.61.125.59`, **orange-cloud OFF initially** (or Full (Strict) SSL mode with proxy ON — `docs/03-architecture.md` says enable TLS via the reverse proxy and set Cloudflare SSL to **Full (Strict)** if orange cloud is on). Also plan `*.` wildcard for merchant subdomains later (same A record to `13.61.125.59`).
3. **TLS termination on the VPS**: add a **Caddy** (or Traefik) container to `~/shopkeet/infra/docker-compose.yml` publishing `80/443` and reverse-proxying `api.<domain>` → `api:3001`. Caddy auto-issues/renews Let's Encrypt certs (set `APP_BASE_DOMAIN` in `api.env` to the real domain too). Do NOT proxy `/metrics` publicly (it's bearer-token gated; keep it blocked at the firewall/proxy regardless).
4. **Security group / firewall**: open `80` and `443` to the internet for the VPS only after Caddy is up; everything else (5432, 6379, 3001, 9090, 3000) stays localhost-bound.
5. Optional: enable **Cloudflare R2 media** by adding the `R2_*` vars to `api.env` and re-running `docker compose up -d` (mounts `/media/*` routes).

### Redeploying after code changes

```bash
# rsync/tar the new apps/api source to /home/ubuntu/shopkeet/apps/api, then:
cd /home/ubuntu/shopkeet/infra
docker compose up -d --build
# migrations are idempotent: docker exec shopkeet-api /usr/local/bin/shopkeet-migrate up
```