# Skill: Build the Shopkeet backend

## When to use this skill

Any task that adds or changes code under `apps/api/`. Load `.agents/rules/backend-constraints.md` and `.agents/rules/conventions.md` alongside this skill — they're not optional context, they're enforced on everything built here.

Exact table definitions live in `docs/schema.sql`; the full endpoint contract lives in `docs/api-reference.md`. This skill sequences the work and states what "done" means at each step — it doesn't repeat the DDL or the full endpoint list.

## How to use this skill

Work through the phases below **in order**. Do not start phase N+1 until phase N's acceptance criteria pass via an automated test. If a phase's acceptance criteria fail:

1. Do not proceed to the next phase.
2. Re-read the relevant section of `docs/04-agent-build-spec.md` for the intended behavior.
3. Fix the implementation, re-run the test, repeat until it passes.
4. Only then move on. A phase that "mostly works" is not done.

## Phase 0 — Scaffolding

- `docker-compose.yml` with `postgres`, `redis`, `api`, `web` services.
- Fiber app with `GET /healthz` → `200 {"status":"ok"}`; config loaded from env.
- `golang-migrate` wired up with an empty initial migration.
- CI: `go vet`, `go test`.

**Done when:** `docker compose up` brings up all services with no errors, `/healthz` returns 200, CI passes.

## Phase 1 — Tenants & Auth

- Apply `tenants`, `merchant_users` from `docs/schema.sql`.
- `POST /api/v1/auth/signup` — creates a tenant + owner user in one transaction.
- `POST /api/v1/auth/login` — issues a JWT (`tenant_id`, `user_id`, `role`).
- Auth middleware: validates JWT, runs `SET LOCAL app.current_tenant` inside the request's transaction.

**Done when:** a new merchant can sign up and log in; a test creates two tenants and asserts cross-tenant reads return nothing.

## Phase 2 — Media Library (Cloudflare R2)

- Apply `media_assets` from `docs/schema.sql`.
- `internal/media` generates presigned R2 PUT URLs (`POST /media/upload-url`); the browser uploads bytes directly to R2, never through the Go API; `POST /media` confirms and records metadata.
- Endpoints per `docs/api-reference.md` §Media.

**Done when:** a file uploaded through this flow is retrievable at its public URL; request logs show no large file bodies passing through the API; cross-tenant access to another tenant's assets is rejected.

## Phase 3 — Catalog

- Apply `categories`, `products`, `product_categories`, `product_images` from `docs/schema.sql`.
- Endpoints per `docs/api-reference.md` §Catalog. Product images attach `media_assets` from Phase 2 via `product_images`, with `sort_order`.
- Postgres `tsvector` full-text search on `name`/`description` — no external search service yet.

**Done when:** Merchant A creates a product with images; Merchant B's storefront and admin API cannot see or edit it; only `status=active` products are publicly listed; images return in gallery order.

## Phase 4 — Cart & Inventory

- Apply `carts`, `cart_items` from `docs/schema.sql`.
- Redis: `SETNX shopkeet:reserve:{product_id}:{unit_index}` with a 15-minute TTL on add-to-cart.
- Checkout-time confirmation: `SELECT inventory_count FROM products WHERE id = $1 FOR UPDATE` inside a transaction — the real guard, not the Redis reservation.
- Endpoints per `docs/api-reference.md` §Cart.

**Done when:** two concurrent add-to-cart requests for the last unit of stock — exactly one succeeds at checkout, not at add-to-cart time.

## Phase 5 — Checkout & Orders (Cash on Delivery)

- Apply `orders`, `order_items` from `docs/schema.sql`.
- `payments` module implements `PaymentProvider` with one provider: `cod`. No external gateway calls.
- `POST /checkout` validates stock (Phase 4's `FOR UPDATE` transaction), creates the order (`payment_method='cod'`), decrements inventory, clears the cart, emits `order.created`.
- `PATCH /orders/:id/status` moves the order through statuses; `delivered` sets `payment_status='paid'` and emits `order.paid`.

**Done when:** a guest completes COD checkout, the order appears in admin as `pending`/`pending`, inventory decrements exactly once, and the merchant can move it to `delivered` + `paid`.

## Phase 6 — Content & Page Builder

- Apply `posts`, `templates`, `sections`, `redirects` from `docs/schema.sql`. Endpoints per `docs/api-reference.md` §Content & Page Builder.
- Three distinct concepts — don't collapse them: **posts** (one-off content: `page`, `blog_post`), **templates** (rendering rules applied across many instances: `product`, `product_archive`, `cart`, `404`, `order_confirmation`), **sections** (global chrome not tied to a route: `header`, `footer`, and later `announcement_bar`/`popup`).
- Checkout is NOT part of the template system — color/logo theming only, wired directly in `web`.
- Frontend renders all three via **Puck** (`@puckeditor/core`); `onPublish` saves Puck's JSON straight into `layout` — no transformation needed.
- On a post's `route` change, write a `redirects` row from the old path; Next.js middleware checks `GET /redirects/lookup` before falling through to the `404` template.
- Populate `meta_title`/`meta_description` (posts, products) and `og_image_id` (posts) from the start.

**Done when:** editing the `page` post at `route='/'` reflects on the storefront without a deploy; every product renders through the single `product` template; a route change produces a working redirect; a nonexistent route renders the `404` template.

**Deferred, not required for v1:** `blog_archive`/`search_results` templates, `announcement_bar`/`popup` sections, revision history, coming-soon/password-protected mode.

## Phase 7 — Observability & hardening

- `/metrics` (Prometheus format), structured JSON logging, Grafana dashboard in `infra/`.
- Consistent JSON error shape; recover middleware so no panic reaches the client.

**Done when:** dashboards show real traffic from a local run; a malformed request returns the standard error shape, not a raw stack trace.

## Internal events (build in from Phase 5 onward)

Emit events through `internal/platform/events` for anything meaningful: `order.created`, `order.paid`, `product.updated`, `inventory.low`. This is the seam a future `webhooks` module subscribes to without touching `catalog`, `orders`, or `content`.

## Definition of done for the whole backend

All eight phases' acceptance criteria pass, end to end, on a fresh `docker compose up`: signup → subdomain → upload product photos to R2 → add product → customize homepage and product page via Puck → complete a COD purchase as a test customer → see it in Grafana.
