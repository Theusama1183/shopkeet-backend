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
- **Phase 1 — Tenants & Auth: NOT STARTED.** Next up: tenants/merchant_users schema + RLS, `POST /auth/signup` + `/auth/login`, JWT middleware + `SET LOCAL app.current_tenant` RLS session, cross-tenant isolation tests.

## Working agreement for any agent in this repo

- Don't start the next phase until the current one's acceptance criteria pass with an automated test, not just a manual check.
- If a task seems to require breaking a non-negotiable constraint, stop and flag it — don't quietly route around it.
- If a change alters an API shape, event name, or schema, update `docs/api-reference.md` or `docs/schema.sql` in the same change, not as a follow-up.
- Nothing on `docs/01-mission.md`'s "explicitly not building" list gets built without being asked, regardless of how natural it seems as a next step.
