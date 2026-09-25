# Shopkeet — Technology Stack

This stack is deliberately leaner than a "Shopify-at-scale" architecture. At planning stage, serving solo/small merchants, the biggest risk isn't insufficient scale — it's building infrastructure a small team can't operate. Every choice below is open source, and every piece has a stated trigger for when to reconsider it.

## Guiding principles

1. **Open source only, with exceptions flagged explicitly** — no proprietary databases or licensed infrastructure software without a stated reason. Two exceptions exist today, both deliberate: Cloudflare (edge/DNS/DDoS) and Cloudflare R2 (object storage) — see below.
2. **Modular monolith first, split later.** One well-structured backend service, internally divided into clear modules (catalog, cart, orders, tenants). This is far easier for a small team to build, deploy, debug, and reason about than a microservices mesh — and it can be split into real services later along the same module boundaries, without a rewrite.
3. **Boring, proven technology** wherever the problem isn't actually novel. Save complexity budget for the things that make Shopkeet different (the page builder, per-merchant payment routing).
4. **Every piece of infra you add is a piece you have to operate.** Kafka, service meshes, and distributed SQL are excellent tools for problems Shopkeet doesn't have yet.

## The stack

| Layer | Choice | Why |
|---|---|---|
| Frontend (storefront + admin) | **Next.js** (React) | Open source, strong DX, built-in support for the ISR/SSR mix a storefront needs, mature middleware for tenant routing. |
| Backend API | **Go**, single framework (**Fiber** or **Gin** — pick one, don't mix) | Fast, simple deployment (single static binary), good concurrency for a small team to reason about. One framework, not three, so there's one set of idioms to learn. |
| Backend structure | **Modular monolith** | Internal packages: `tenants`, `media`, `catalog`, `cart`, `orders`, `payments`, `content`. One deployable, one database connection pool, one thing to monitor. |
| Primary database | **PostgreSQL** | Open source, rock-solid ACID guarantees, and Row-Level Security gives real per-tenant data isolation without a distributed database. Handles catalog, orders, content, and tenant config — one database to operate, not three. |
| Cart / hot data cache | **Redis** | Cache-aside for cart state and inventory counts. Postgres remains the source of truth; Redis just makes reads fast. Avoids the complexity of treating a cache as authoritative. |
| Background jobs / async | **Asynq** (Go, Redis-backed) | Handles things like "send order confirmation," "update search index," "webhook retries" — without standing up a Kafka cluster. |
| Search & filtering | **Postgres full-text search** to start; **Meilisearch** (open source) when catalog size or filtering needs outgrow it | Zero extra infrastructure at launch. Meilisearch is far lighter to self-host than a Typesense cluster when the time comes. |
| Object storage / media | **Cloudflare R2** (proprietary — see note below) | Zero egress fees, which matters a lot for a storefront serving product images to customers. S3-compatible API, so it's not a proprietary SDK lock-in — pointing at a different S3-compatible endpoint later (MinIO, Backblaze B2) is a low-friction swap if ever needed. |
| Page builder | **Puck** (`@puckeditor/core`, open source, MIT) | Gives the drag-and-drop editor, undo/redo, and prop panels for free, on top of the same JSON-block data model already used for `posts`/`templates`/`sections`. Building this UI well from scratch is a multi-month project not worth a small team's time. |
| Auth | **Auth.js** (formerly NextAuth) or **Ory Kratos** if you want it fully decoupled from the frontend | Open source, self-hostable, handles merchant + storefront-customer auth without a proprietary IDP. |
| Payments | **Cash on Delivery only for v1** — no gateway | No PCI scope, no proprietary payment processor dependency at all. An online provider (Stripe or otherwise) is a later, explicit addition behind the `PaymentProvider` interface — not built now. |
| Hosting / infra | **Coolify** on a single VPS (Hetzner, DigitalOcean), fronted by **Cloudflare** (proprietary — see note below) | Coolify gives git-push deploys, a managed Traefik proxy with per-app TLS, and — since the frontend will live on it too — a single deploy surface (no Vercel). **Re-decided 2026-09-25**: the original compose+Caddy-for-RAM preference was reversed; see `03-architecture.md` §7 and `backend-constraints.md`. Postgres/Redis currently remain manual containers on Coolify's network (hybrid); merchant custom domains are deferred under Coolify's proxy. |
| CI/CD | **GitHub Actions** | Free tier, no separate CI infra to run. |
| Observability | **Prometheus + Grafana + Loki** | Fully open source, self-hostable, industry standard. |

### Non-open-source dependencies (used deliberately, flagged explicitly)

Two proprietary services are in the stack today, both from Cloudflare, both chosen for a specific practical reason rather than by default:

- **Cloudflare (DNS, edge caching, DDoS protection)** — free/pro tier, sits in front of `shopkeet.com`. No open-source edge network offers the same global anycast DDoS protection without operating your own points of presence, which is out of scope for a small team.
- **Cloudflare R2 (object storage for product/media images)** — chosen specifically for its zero-egress-fee pricing, which matters directly for a storefront serving images to customers (AWS S3 and GCS both charge meaningfully for egress at scale). Because R2 speaks the S3 API, this is not a hard lock-in: the `media` module's integration code targets a generic S3-compatible endpoint, so switching to a self-hosted, fully open-source alternative like **MinIO** later is a configuration change, not a rewrite.

Payments carry no proprietary dependency at all in v1 — Cash on Delivery needs no gateway. If an online payment method is added later, that will be a new, explicitly flagged exception at that time, not before.

## What we're deliberately NOT using yet (and when to revisit)

| Not using now | Reconsider when |
|---|---|
| CockroachDB / distributed SQL | You need multi-region strong consistency or a single Postgres instance can't handle write volume even after read-replicas and connection pooling |
| Kafka | Async job volume outgrows what Asynq/Redis can handle, or you need durable event replay across many independent consumers |
| Kubernetes / service mesh (Istio, Linkerd) | You have enough independently-scaled services that a single Docker Compose host (or a couple) genuinely can't fit them, and you have someone dedicated to operating the cluster |
| gRPC / API Gateway (KrakenD, Envoy) | The monolith is split into multiple real services that need to talk to each other at low latency, high volume |
| Typesense cluster | Meilisearch (single node) can't keep up with catalog size or query volume |
| Multiple Go frameworks | A specific service has performance needs the others genuinely don't share (unlikely before you have real production traffic data) |

The theme: every one of these is a good tool for a *later* problem. Adding them now would mean spending a small team's limited time operating infrastructure instead of shipping the product that makes Shopkeet worth using.
