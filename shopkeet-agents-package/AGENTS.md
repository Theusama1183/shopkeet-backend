# AGENTS.md — Shopkeet

Read this file first, every session, before writing any code. It's the entry point for any agent working in this repository — Antigravity, Claude Code, Cursor, or Codex all read `AGENTS.md` the same way.

## What this project is

Shopkeet is a multi-tenant e-commerce SaaS platform for solo/small independent merchants, built on an open-source stack. Full context: `docs/01-mission.md`.

## Source of truth — read in this order before writing code

1. `docs/01-mission.md` — what we're building, for whom, and what's explicitly out of scope
2. `docs/02-tech-stack.md` — the chosen stack and why
3. `docs/03-architecture.md` — system design and deployment topology
4. `docs/04-agent-build-spec.md` — backend build plan, phase by phase (Go API)
5. `docs/05-frontend-agent-spec.md` — frontend build plan and design rules (Next.js)
6. `docs/06-ai-development-rules.md` — how multiple agents coordinate on this codebase
7. `docs/schema.sql` — the full database schema, every table, in one file
8. `docs/api-reference.md` — every REST endpoint, in one place

Always-loaded workspace rules live in `.agents/rules/`. Step-by-step implementation guides live in `.agents/skills/`. Load the relevant skill before starting backend work rather than working from memory of this file alone.

## Non-negotiable constraints (condensed — full reasoning in `docs/04-agent-build-spec.md §0`)

- One Go backend service, internally modular (`tenants`, `media`, `catalog`, `cart`, `orders`, `payments`, `content`). No microservices.
- One Go web framework (Fiber). PostgreSQL only. Redis is cache/queue only — never the only copy of a fact that matters.
- No Kafka, gRPC, API gateway, Kubernetes, service mesh, or GraphQL for the internal API.
- Every tenant-scoped table has `tenant_id` + Row-Level Security from the migration that creates it.
- Payments: Cash on Delivery only for v1. No payment gateway integration.
- Object storage: Cloudflare R2 only, via presigned URLs — not open source, a deliberate flagged exception (see `docs/02-tech-stack.md`).
- Page builder: Puck (`@puckeditor/core`), not a hand-built editor.
- Deployment: Coolify on a single VPS; Traefik handles TLS automatically per domain.

Full, enforceable version of these: `.agents/rules/backend-constraints.md`.

## The agent team model

This project is built the way a small software house splits work — full roles and ground rules in `docs/06-ai-development-rules.md`. In short:

- **Architect** owns the docs in this repo and resolves ambiguity — nobody else overrides a documented decision unilaterally.
- **Backend** implements `.agents/skills/backend-build.md`, one phase at a time.
- **Frontend** implements `docs/05-frontend-agent-spec.md` against the contract in `docs/api-reference.md`.
- **QA** verifies each phase's acceptance criteria independently — the agent that built a feature doesn't self-certify it done.

## Build progress (this file is the running log — update after every phase)

- **Phase 0 — Scaffolding: DONE** (verified: `go build ./...` ✓, `go vet ./...` ✓, `go test ./...` ✓, `migrations up: done` against VPS Postgres via SSH tunnel, `/healthz` → `200 {"status":"ok"}`).
  - Stack confirmed locally: Go 1.27 + Fiber + pgx (pgxpool) + Redis + golang-migrate (embedded iofs) + Asynq (background queue, Redis-backed — **not** BullMQ: BullMQ is Node-only, spec `02-tech-stack.md` picks Asynq for the Go backend).
  - VPS `~/infra/docker-compose.yml`: postgres:16-alpine + redis:7-alpine, bound to 127.0.0.1 only, fresh named volumes. Local dev connects via SSH tunnel (`-L 5432 -L 6379`). Old payload.json / shopkeet_apps deleted.
  - No frontend (deliberate per user; CI + compose are backend-only).
- **Phase 1 — Tenants & Auth: DONE** (verified: `go build ./...` ✓, `go vet ./...` ✓, `go test ./...` ✓, `migrations up: done` against VPS Postgres via SSH tunnel, cross-tenant RLS acceptance test passes).
  - Schema in same migration as table: `tenants`, `merchant_users` + `tenant_isolation` policy (`0002_tenants_auth.*`), `FORCE ROW LEVEL SECURITY`.
  - RLS gotcha solved: the Postgres `POSTGRES_USER` (`shopkeet`) is a superuser and bypasses RLS silently. `0003_app_role` adds a dedicated **non-superuser** `shopkeet_app` role that owns the tenant tables; the API and tests connect as `shopkeet_app` (`DATABASE_URL=…shopkeet_app`), not `shopkeet`. `0004_default_grants` auto-grants future phase tables to `shopkeet_app`.
  - Endpoints live: `POST /api/v1/auth/signup`, `POST /api/v1/auth/login` (registered by `auth.RegisterRoutes` in `cmd/api/main.go`). JWT middleware `TenantMW` sets `app.current_tenant` per request.
  - Acceptance test: `TestTenantRLSIsolation` (skips unless `DATABASE_URL` set; run as `shopkeet_app`).
- **Phase 2 — Media Library (Cloudflare R2): DONE** (verified: `go build ./...` ✓, `go vet ./...` ✓, `go test ./...` ✓, `migrations up: done` against VPS Postgres via SSH tunnel, cross-tenant media RLS acceptance test passes).
  - `0005_media_assets.*` — `media_assets` + `tenant_isolation` policy **in the same migration** + `FORCE ROW LEVEL SECURITY`; owner `shopkeet_app` (matches 0003 model).
  - `internal/media/` — `R2` (AWS SDK S3 signing client, endpoint `https://{account_id}.r2.cloudflarestorage.com`) behind the `ObjectStore` interface (fake in tests), `Service` handlers + `RegisterRoutes` mounting under `auth.TenantMW` (RLS-scoped request tx): `POST /media/upload-url`, `POST /media`, `GET /media`, `DELETE /media/:id`.
  - Upload flow (API never sees bytes): client → presigned PUT URL → direct R2 upload → POST /media records metadata. r2_key is `{tenant_id}/{uuid}.{ext}`; a key whose first path segment isn't the caller's tenant is refused 403 — defense in depth on top of RLS.
  - R2 envs: `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_BUCKET_NAME`, `R2_PUBLIC_URL` (config.Load requires all-or-none; `/media` routes mount only when configured).
  - Acceptance test: `TestMediaRLSIsolation` (`internal/media/media_test.go`; skips without `DATABASE_URL`, run as `shopkeet_app`) — upload/confirm/list/delete through live HTTP handlers + RLS; asserts cross-tenant list empty, cross-tenant delete 404 (no R2 delete fires), stolen-key confirm 403.
- **Phase 3 — Catalog: DONE** (verified: `go build ./...` ✓, `go vet ./...` ✓, `go test ./...` ✓ against VPS Postgres via SSH tunnel, cross-tenant catalog RLS acceptance test passes).
  - `0006_catalog.*` — `categories`, `products` (with generated `search_vector` + GIN index), `product_categories`, `product_images` — every table gets its `tenant_isolation` policy **in the same migration** + `FORCE ROW LEVEL SECURITY`, owner `shopkeet_app`.
  - Tenant resolution per `docs/03-architecture.md` §2: Next.js middleware resolves the tenant from the hostname and forwards it; storefront endpoints validate `X-Tenant-ID` via `auth.PublicTenantMW` (opens the RLS-scoped request tx, like `TenantMW` but no JWT). `auth.PublicOrAdminMW` serves dual-role routes (`GET /products/:id`): valid Bearer JWT → admin view (any status), otherwise public active-only.
  - `internal/catalog/` — handlers + `RegisterRoutes` on the `/api/v1` router: `GET /products` (public, active-only, `?search=` tsvector + `?category=` slug filters), `GET /products/:id`, `POST/PATCH/DELETE /products/:id` (admin), `POST /products/:id/images` + `DELETE /products/:id/images/:imageId` (admin), `GET /categories` (public). Product JSON embeds ordered `images` (join to `media_assets`) and `categories`.
  - PG gotcha handled: a failing statement (unique violation → 409/400) aborts the request tx, so the middleware's final Commit would 500 ("commit unexpectedly resulted in rollback"). Conflict-prone writes run under a `SAVEPOINT` (`savepoint` helper) so a predicted 4xx keeps the tx usable.
  - Acceptance test: `TestCatalogRLSIsolation` (`internal/catalog/catalog_test.go`; skips without `DATABASE_URL`, run as `shopkeet_app`) — active-only publicity, gallery ordering, search, admin-vs-public view, and full cross-tenant isolation (B cannot list/edit/delete/fetch A's products).
- **Phase 4 — Cart & Inventory: DONE** (verified: `go build ./...` ✓, `go vet ./...` ✓, `go test ./...` ✓ against VPS Postgres via SSH tunnel, cross-tenant cart RLS acceptance test passes; live Redis reservation test passes against VPS Redis).
  - `0007_cart.*` — `carts`, `cart_items` + `tenant_isolation` policy **in the same migration** + `FORCE ROW LEVEL SECURITY`, owner `shopkeet_app`. Adds the invariants the API relies on: `UNIQUE (tenant_id, customer_session)` (one cart per guest session) and `UNIQUE (cart_id, product_id)` (one line per product; adds merge quantity).
  - Guest session resolution: `auth.CustomerMW` resolves the tenant from `X-Tenant-ID` like `PublicTenantMW`, plus the guest `customer_session` from the `shopkeet_session` cookie or `X-Customer-Session` header — minting (and echoing/minting cookie) when absent. Session id rides in `c.Locals("customer_session")`.
  - `internal/cart/` — handlers + `RegisterRoutes` on `/api/v1`: `GET /cart` (returns `{"cart": null}` for a fresh session), `POST /cart` (creates cart on first use, merges same-product quantity, only `status=active` products cartable), `PATCH /cart/items/:id`, `DELETE /cart/items/:id`. Cart JSON embeds product info + line totals + cart total.
  - Reservation (spec Phase 4): best-effort Redis `SETNX shopkeet:reserve:{product_id}:{unit_index}` (TTL 15min) on add-to-cart via `cart.Reserver` (`RedisReserver` / `NoopReserver` when `REDIS_URL` unset). Strictly cache — checkout's `SELECT ... FOR UPDATE` (Phase 5) is the authoritative anti-oversell guard, so add-to-cart never gates on stock (matching the spec's acceptance).
  - New dep: `github.com/redis/go-redis/v9`.
  - Acceptance tests (`internal/cart/`): `TestCartRLSIsolation` — merge/patch/delete + totals, zero-stock product cartable at add time, archived product rejected, cross-session and cross-tenant isolation (B's PATCH on A's line 404s), session minting sets cookie + echoes `X-Customer-Session`; `TestRedisReserver` (skips unless `REDIS_URL` set) — key + TTL verified against live Redis.
- **Phase 5 — Checkout & Orders (COD): DONE** (verified: `go build ./...` ✓, `go vet ./...` ✓, `go test ./...` ✓ against VPS Postgres via SSH tunnel, cross-tenant checkout/orders RLS acceptance test passes).
  - `0008_orders.*` — `orders`, `order_items` + `tenant_isolation` policy **in the same migration** + `FORCE ROW LEVEL SECURITY`, owner `shopkeet_app`. `order_items` embeds `unit_price_cents`/`currency` as a line-item snapshot (price history survives price edits); `orders` holds `total_cents` + `customer_name/phone/email` (phone NOT NULL — the guest's order-verification key).
  - `internal/payments/` — the gateway seam: `Provider` interface + `Registry` (multi-provider methods list). v1 ships only `CODProvider` (name `cod`, creates orders `payment_status=pending`; `paid` is set by fulfillment, see below).
  - `internal/orders/` — `Checkout` (`POST /checkout`, under `auth.CustomerMW`): loads the guest cart, locks every carted product `SELECT ... FOR UPDATE` (the authoritative anti-oversell guard — two concurrent checkouts for the last unit serialize, second gets 409), checks `status=active` + stock inline, runs the COD provider, inserts order + line items (snapshotted prices), decrements `inventory_count`, clears the cart, emits `order.created`, all in one RLS-scoped tx. Also `GetOrder` (`GET /orders/:id?phone=`[+`&email=`] — guest lookup keyed on phone, 404 on mismatch), `ListOrders` (`GET /orders` admin, `?status=` filter), `UpdateStatus` (`PATCH /orders/:id/status` admin; `pending→confirmed→shipped→delivered`, `delivered` sets `payment_status='paid'` + emits `order.paid`, `pending/confirmed→cancelled`; skip-a-step → 400).
  - In-process event bus `internal/platform/events` (`events.Bus/Subscribe/Emit`) surfaces `order.created` / `order.paid`; main.go subscribes a log handler (the seam for future webhooks/Asyncq workers — events are best-effort in process for now).
  - Acceptance test: `TestOrdersRLSIsolation` (`internal/orders/orders_test.go`; skips without `DATABASE_URL`, run as `shopkeet_app`) — two guests race for 3 units (2+2): first checkout 201 (pending/pending/cod, total = 2×p1 + 1×p2, 2 items), inventory decremented exactly once, cart cleared; second guest 409 at checkout (not at add-to-cart time); `order.created` emitted once; lookup 200/404 by phone/email; admin list + status filter; status chain through to delivered→paid with `order.paid` emitted once; tenant B sees zero orders and can't fetch A's order by session+phone.

## Working agreement for any agent in this repo

- Don't start the next phase until the current one's acceptance criteria pass with an automated test, not just a manual check.
- If a task seems to require breaking a non-negotiable constraint, stop and flag it — don't quietly route around it.
- If a change alters an API shape, event name, or schema, update `docs/api-reference.md` or `docs/schema.sql` in the same change, not as a follow-up.
- Nothing on `docs/01-mission.md`'s "explicitly not building" list gets built without being asked, regardless of how natural it seems as a next step.
