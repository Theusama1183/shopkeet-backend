# Shopkeet — System Architecture (v1, Open Source, Lean)

This replaces the original "Shopify-at-scale" design with an architecture sized for where Shopkeet actually is: planning stage, targeting solo/small merchants, single-founder-or-small-team operable. It keeps the ideas from the original design that were genuinely good (tenant isolation via `tenant_id`, JSON-based page builder, cache-aside inventory) and drops the infrastructure that isn't earned yet (CockroachDB, Kafka, service mesh, multi-framework microservices).

## 1. System topology

```
        [ shopkeet.com traffic ]          [ merchant custom-domain traffic ]
                  │                                       │
                  ▼                                       │
      [ Cloudflare — DNS, edge caching, DDoS ]             │
       (Full Strict TLS if proxied)                        │
                  │                                       │
                  └───────────────────┬───────────────────┘
                                       ▼
                [ Coolify proxy — auto TLS (API app only, today) ]
                                       │
                    ┌──────────────────┴──────────────────┐
                    ▼                                       ▼
      [ Next.js app (storefronts + admin) ]   [ Go API — modular monolith ]
                                                (Coolify-managed app)
                                                modules: tenants, media, catalog,
                                                cart, orders, payments, content,
                                                shipping, discounts, customers,
                                                notifications
                                                   │              │
                                                   ▼              ▼
                                            [ PostgreSQL ]   [ Redis ]
                                          source of truth,     cache + cart
                                        RLS per tenant_id      + Asynq jobs
```

One frontend app, one backend service, one primary database. Redis is a cache/job-queue layer, not a second source of truth.

## 2. Multi-tenancy

Same core idea as the original design, and it's still the right one:

- Every table in Postgres carries a `tenant_id` column.
- **Row-Level Security (RLS)** policies bound to `tenant_id` enforce isolation at the database layer — even a bug in application code can't leak one merchant's data into another's queries.
- Tenant resolution happens in Next.js middleware, based on custom domain or subdomain, and is passed through to the Go API on every request (as a header or signed token), which sets it for the RLS session.

```ts
// middleware.ts
export function middleware(req: NextRequest) {
  const hostname = req.headers.get("host"); // e.g. merchant-a.shopkeet.com
  const tenantId = getTenantFromHostname(hostname);
  return NextResponse.rewrite(new URL(`/_tenants/${tenantId}${req.nextUrl.pathname}`, req.url));
}
```

## 3. Backend: modular monolith, not microservices

Instead of separate Catalog/Cart/Order *services* with three different frameworks and three different databases, this is **one Go service** with clear internal module boundaries:

```
/internal
  /tenants    — tenant config, domain resolution, subscription status
  /media      — Cloudflare R2 presigned uploads, media_assets (not open source — flagged)
  /catalog    — products, categories, product images, search indexing
  /cart       — cart state, inventory reservation
  /orders     — checkout, order records
  /payments   — Cash on Delivery (default) behind a PaymentProvider interface
  /content    — posts, templates, sections — the page builder domain (see §6)
```

Each module owns its own database tables and exposes a small internal interface to the others — no reaching across module boundaries directly. This is what makes a future split into real services possible without a rewrite: if `cart` ever needs to scale independently, it can be pulled out along the boundary that already exists.

**Why this instead of microservices now:** at solo/small-merchant scale, the operational cost of running and monitoring multiple services, multiple databases, and inter-service networking far outweighs the benefit. A modular monolith gets the code organization benefit without the deployment cost.

## 4. Cart & inventory

The original design's instinct — reserve stock fast, avoid write contention — is right, simplified to fit one database:

- Redis holds a short-TTL reservation (`SETNX` with expiry) when an item is added to cart, so two customers can't both "win" the last unit.
- Postgres remains the actual source of truth for stock counts; Redis reservations are checked against it and confirmed at checkout via a row-level lock (`SELECT ... FOR UPDATE`) inside a transaction.
- If the reservation expires unconfirmed, stock is released automatically.

This avoids the original design's "don't write to disk until checkout" pattern, which risks losing reservation state if Redis has a hiccup — here Postgres is always the fallback truth.

## 5. Checkout & payments

- **Cash on Delivery is the default and only method for v1** — no payment gateway integration required. Checkout records the order with `payment_method = 'cod'`; the merchant collects payment on delivery and marks it paid in the admin.
- The `payments` module is built behind a small `PaymentProvider` interface so an online method can be added later as a second implementation, without touching `orders` or the checkout flow.

This replaces the original "Plugin/Factory Execution Engine" for arbitrary per-merchant payment gateways — a real security-and-maintenance burden — with the simplest possible v1: no external payment processor at all. An online provider (Stripe Connect or a regional gateway) can be added later as an explicit, additive integration if merchants ask for one.

## 6. Content & page builder

Three distinct concepts, not one — see `04-agent-build-spec.md` Phase 6 for the full rationale and `docs/schema.sql` for the tables:

- **Posts** — one-off content a merchant writes by hand: `page`, `blog_post`.
- **Templates** — a rendering rule applied across many instances of data that already exists elsewhere: `product`, `product_archive`, `cart`, `404`, `order_confirmation`. One row per type per tenant; every product renders through the *one* `product` template rather than each having its own hand-built page.
- **Sections** — global chrome not tied to a route: `header`, `footer`, and later `announcement_bar`/`popup` (which carry `placement_rules` for trigger/pages/frequency).

All three store content the same way: structured JSON (never raw HTML), keyed by `tenant_id`, rendered through a block registry.

```json
{
  "tenant_id": "8a3b-41f2",
  "route": "/home",
  "layout": [
    { "id": "hero_block_01", "type": "HeroBanner", "props": { "title": "Summer Collection" } },
    { "id": "product_grid_02", "type": "ProductGrid", "props": { "categoryId": "cat_991", "limit": 4 } }
  ]
}
```

**Editor: [Puck](https://puckeditor.com)** (`@puckeditor/core`, open source, MIT), not a hand-built drag-and-drop canvas. Puck's `config.components` *is* the block registry above — define `HeroBanner`, `ProductGrid`, etc. as plain React components once, and Puck provides the canvas, drag/drop, undo/redo, and prop-editing UI for free. `onPublish` writes Puck's JSON straight into `posts.layout` / `templates.layout` / `sections.layout` — no transformation needed, the schema was designed to match Puck's output directly.

**Checkout is deliberately excluded** from this system — letting merchants freely re-block checkout risks breaking conversion and PCI/legal correctness. It gets color/logo theming only.

## 7. Deployment

**Coolify** (open-source, self-hosted PaaS) manages the API application on the VPS — a deliberate choice made after weighing the RAM trade-off discussed earlier; Coolify-managed deploys and Coolify's proxy/TLS handling won out over running everything as plain containers.

> Note on "git-push auto-deploy": `settings.is_auto_deploy_enabled=true` is set on the app, but **no Git source/webhook is connected** (`source_id=0`, `webhook_token_url=null` — verified against the Coolify API 2026-10-04). Git pushes therefore never trigger a build; releases are deployed manually via `POST /api/v1/deploy?force=true` (see `p21-deploy.ps1`). If auto-deploy is ever wanted, it requires connecting the GitHub source / webhook in Coolify first — the settings flag alone is inert.

```
Internet → Coolify's proxy (Traefik, auto TLS)
             └── api.shopkeet.com  → Coolify-managed Go API app
```

- **API app** is Coolify-managed (built from the git repo, built via `POST /api/v1/deploy?force=true`). Despite `settings.is_auto_deploy_enabled=true`, no Git source/webhook is connected to the app (`source_id=0`), so pushes to `main` do **not** trigger a build — deploy manually after every release (`p21-deploy.ps1`). Coolify's proxy issues and renews its TLS certificate.
- **Data layer is currently a hybrid, not fully migrated:** Postgres and Redis still run as the original manually-managed containers (`shopkeet-postgres`, `shopkeet-redis`), attached to Coolify's Docker network by alias so the Coolify-managed API can reach them — they are not Coolify **services**. This means the VPS is running Coolify's own management stack (including its own `coolify-db`/`coolify-redis`) *alongside* the manual data containers. Worth a conscious decision at some point: either bring Postgres/Redis fully under Coolify as services too (one consistent management path, one more data-migration step), or accept the hybrid permanently. Neither is wrong, but drifting into it by default is worth avoiding.
- **`apps/web` (Next.js) and merchant custom domains** aren't deployed yet. When that work starts, decide explicitly whether new custom domains are attached via a Coolify API call per domain (straightforward, matches how the API app is managed today) or via Caddy's `on_demand_tls` running alongside Coolify (fully automatic, no per-domain API call needed, but a second proxy layer to reason about). Don't let this get decided implicitly by whatever the frontend agent finds easiest.
- **Cloudflare** sits in front of `shopkeet.com` for DNS, edge caching, and DDoS protection — no Enterprise tier needed. If Cloudflare's proxy (orange cloud) is enabled, set SSL mode to **Full (Strict)**.
- **GitHub Actions** runs tests/builds on every PR; Coolify's own deploy handles the release.

**Notifications provider (production):** **Resend** (not open source — flagged, same category as Cloudflare/R2) is the live provider — `RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL` set, SMTP env vars removed so Resend wins the provider-resolution order. SMTP (e.g. Mailpit) is for local/dev only — it's a testing catcher, not a real delivery path, and must never be the configured provider in a live environment a real merchant depends on. See `07-expansion-build-spec.md` Phase 12 for the full provider design.

**Media storage sits outside this VPS entirely.** Product and content images live in **Cloudflare R2**, not on local disk — the Go API issues short-lived presigned upload URLs, and the browser uploads bytes directly to R2 (see `04-agent-build-spec.md` Phase 2). This keeps the VPS stateless for media and avoids needing a backup strategy for locally-stored files. R2 is the second deliberate non-open-source exception in this stack, alongside Cloudflare itself — see `02-tech-stack.md` for the reasoning.

## 8. Observability

- **Prometheus** scrapes metrics from the Go API and Postgres exporter.
- **Grafana** for dashboards.
- **Loki** for log aggregation.
All open source, all fit comfortably on the same infrastructure as the app itself at this scale.

## 9. Growth path — when to revisit this design

| Signal | Next step |
|---|---|
| Postgres write load becomes the bottleneck even after indexing/read-replicas | Consider read-replicas first, then (only if truly needed) a distributed SQL layer |
| One module (e.g. `cart`) needs to scale independently of the rest | Extract it along its existing internal boundary into its own service |
| Async job volume outgrows Redis/Asynq | Introduce a proper message broker (NATS is a lighter open-source option than Kafka) |
| Single-VPS deployment can't fit the workload | Move to a small Kubernetes cluster or managed container platform |
| Catalog search outgrows Meilisearch single-node | Move to a Meilisearch or Typesense cluster |

Each step is additive and triggered by an actual, measured constraint — not anticipated in advance.
