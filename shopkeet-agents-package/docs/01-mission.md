# Shopkeet — Mission & Product Vision

## What we're building

Shopkeet is a multi-tenant, SaaS e-commerce platform that lets solo and small independent merchants launch a modern online store in minutes — without hiring a developer, paying enterprise SaaS fees, or being locked into a closed platform's ecosystem.

We host it. Merchants sign up, get a store, and sell. One codebase, one platform, serving many independent shops — the same model as Shopify, but built on an open-source stack we control end to end.

## The problem

Small merchants today are stuck choosing between:
- **Expensive, closed platforms** (Shopify, BigCommerce) — powerful, but fees scale painfully at low volume, and merchants are locked into proprietary apps/themes.
- **Self-hosted open-source carts** (WooCommerce, Saleor self-hosted) — cheaper, but the merchant becomes their own sysadmin: hosting, security patches, scaling, backups. Most solo sellers can't or don't want to do this.
- **No-code page builders bolted onto payment links** — fast to start, but shallow: weak catalog/inventory management, poor scaling to real order volume.

There's a gap for a platform that has the **simplicity and cost structure small merchants need**, with **the hosted convenience of Shopify** and **the transparency/cost-control of an open-source stack**.

## Who it's for

Solo and small independent merchants — one person or a small team, selling physical or digital goods, who want:
- A store live fast, with minimal setup
- Predictable, low cost at low sales volume
- No server/infrastructure to manage themselves
- Enough flexibility (custom page layouts, their own payment account) to look and feel like their own brand, not a template clone

This is **not**, at least initially, aimed at enterprise brands, high-volume retailers, or agencies managing dozens of client stores — those bring requirements (dedicated infra, custom SLAs, complex integrations) that would pull us away from serving the core user well.

## What makes Shopkeet different

- **Hosted simplicity, open-source backbone.** Merchants get a zero-ops SaaS experience; we get full cost control and no per-seat licensing fees from our own infrastructure vendors.
- **Fast time-to-store.** Sign up → configure a storefront via a drag-and-drop page builder → start selling, without touching code.
- **Own-your-payments.** Each merchant connects their own payment account (via Stripe Connect), so they're paid directly and we're not a money-transmitter — this keeps compliance scope sane for us and trust high for them.
- **Built to stay simple.** We deliberately avoid enterprise-scale complexity (service meshes, distributed databases, multi-region clusters) until real usage demands it — see the tech stack and architecture docs.

## What we are explicitly NOT building (yet)

Scope discipline matters more than feature breadth at this stage. Out of scope for v1:
- Self-hosted/on-prem deployment option for merchants
- Multi-warehouse / complex B2B inventory logic
- Custom app marketplace / third-party developer ecosystem
- Agency/multi-client management tooling
- Enterprise SLAs, dedicated infrastructure, multi-region failover

These may become relevant later, but designing for them now would slow down getting a working product in front of real small merchants.

## Early success metrics

Since this is idea/planning stage, "success" for the first phase means:
- A merchant can sign up, build a basic storefront, list products, and take a real payment end-to-end
- Time from signup to "store is live" is minutes, not hours
- Infrastructure cost per idle/low-volume tenant is low enough that free or near-free tiers are viable
- The codebase is simple enough for a small team (or solo founder) to fully understand and operate
