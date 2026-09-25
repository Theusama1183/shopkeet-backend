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
                [ Caddy — on-demand TLS per domain ]
                                       │
                    ┌──────────────────┴──────────────────┐
                    ▼                                       ▼
      [ Next.js app (storefronts + admin) ]   [ Go API — modular monolith ]
                                                modules: tenants, media, catalog,
                                                cart, orders, payments, content
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

**Docker Compose + Caddy** is the deployment layer, running directly on a single VPS — chosen over Coolify specifically to avoid its management-stack RAM overhead competing with the app's own containers (Postgres, Redis, API, Next.js, Prometheus/Grafana) on a small instance:

```
Internet → Caddy (on-demand TLS, ask-endpoint-gated, via Let's Encrypt)
             ├── shopkeet.com, *.shopkeet.com  → Next.js container
             ├── any merchant custom domain     → same Next.js container
             └── api.shopkeet.com               → Go API container
```

- Next.js and the Go API are plain containers in `docker-compose.yml`. Deploys are `scp`/`ssh` + `docker compose up -d` — no git-push-to-deploy convenience, but no PaaS management overhead either.
- **Caddy's on-demand TLS** (`on_demand_tls` + an `ask` endpoint) issues a certificate the first time a request arrives for a domain it doesn't already have one for, instead of requiring every domain pre-listed in config. This is what covers arbitrary merchant custom domains without Coolify or Traefik: the `ask` endpoint is a small Go API route that checks `tenants.custom_domain` and returns 200 only for domains that actually belong to a real tenant — this guard is mandatory, not optional, or the server will happily request (and burn Let's Encrypt's rate limit on) certificates for domains attackers point at it. Pair it with `interval`/`burst` rate limiting on the `on_demand_tls` block.
- **Cloudflare** sits in front of `shopkeet.com` itself for DNS, edge caching, and DDoS protection — no Enterprise tier needed at this stage. If Cloudflare's proxy (orange cloud) is enabled, set SSL mode to **Full (Strict)** so Cloudflare validates Caddy's real certificate instead of accepting anything. Merchant custom domains are typically pointed directly at the VPS by the merchant's own DNS (not through this Cloudflare account), so Caddy issues those certificates directly via the on-demand flow above.
- **GitHub Actions** runs tests/builds on every PR; the manual deploy step handles the actual release.
- If the deploy workflow (not the TLS/RAM concern) becomes painful enough later — e.g. wanting git-push deploys back — Coolify remains a fine thing to add on a bigger VPS at that point. It's not ruled out forever, just not worth its overhead on the current box.

**Trade-off worth knowing:** Caddy's on-demand TLS gives up nothing on the TLS side compared to the Coolify/Traefik approach — the real trade-off is the deploy workflow: manual `ssh`/`docker compose` instead of git-push, in exchange for a materially lighter footprint on a small VPS.

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
