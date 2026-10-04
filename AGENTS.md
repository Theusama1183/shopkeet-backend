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

Always-loaded workspace rules live in `.agents/rules/`. Step-by-step implementation guides live in `.agents/skills/`. **Before writing any new query, scheduled job, route, or migration, read `.agents/rules/known-gotchas.md`** — it consolidates the bug classes that have recurred on this codebase (RLS-scoping in jobs, pgx scan/tx traps, Fiber middleware leakage, psql plumbing). Load the relevant skill before starting backend work rather than working from memory of this file alone.

## Non-negotiable constraints (condensed — full reasoning in `docs/04-agent-build-spec.md §0`)

- One Go backend service, internally modular (`tenants`, `media`, `catalog`, `cart`, `orders`, `payments`, `content`, `shipping`, `discounts`, `customers`, `notifications`). No microservices.
- One Go web framework (Fiber). PostgreSQL only. Redis is cache/queue only — never the only copy of a fact that matters.
- No Kafka, gRPC, API gateway, Kubernetes, service mesh, or GraphQL for the internal API.
- Every tenant-scoped table has `tenant_id` + Row-Level Security from the migration that creates it.
- Payments: Cash on Delivery only for v1. No payment gateway integration.
- Object storage: Cloudflare R2 only, via presigned URLs — not open source, a deliberate flagged exception (see `docs/02-tech-stack.md`).
- Page builder: Puck (`@puckeditor/core`), not a hand-built editor.
- Deployment: Coolify manages the API app on a single VPS, but deploys are kicked **manually** (`POST /api/v1/deploy?force=true` → `p21-deploy.ps1`), not by git-push. `is_auto_deploy_enabled=true` is set in Coolify settings, but **no Git source/webhook is connected to the app** (`source_id=0`, `webhook_token_url=null`, verified 2026-10-04), so pushes to `main` never trigger a build. Run the deploy script after every release. Coolify's proxy issues auto TLS. Postgres/Redis are still manual containers attached to Coolify's network, not Coolify services — a known hybrid, not a design goal (see `03-architecture.md` §7).
- Notifications: Resend in production (flagged non-open-source exception). SMTP/Mailpit is dev-only — never the configured provider where a real merchant depends on delivery.

Full, enforceable version of these: `.agents/rules/backend-constraints.md`.

## The agent team model

This project is built the way a small software house splits work — full roles and ground rules in `docs/06-ai-development-rules.md`. In short:

- **Architect** owns the docs in this repo and resolves ambiguity — nobody else overrides a documented decision unilaterally.
- **Backend** implements `.agents/skills/backend-build.md`, one phase at a time.
- **Frontend** implements `docs/05-frontend-agent-spec.md` against the contract in `docs/api-reference.md`.
- **QA** verifies each phase's acceptance criteria independently — the agent that built a feature doesn't self-certify it done.

## Working agreement for any agent in this repo

- Don't start the next phase until the current one's acceptance criteria pass with an automated test, not just a manual check.
- If a task seems to require breaking a non-negotiable constraint, stop and flag it — don't quietly route around it.
- If a change alters an API shape, event name, or schema, update `docs/api-reference.md` or `docs/schema.sql` in the same change, not as a follow-up.
- Nothing on `docs/01-mission.md`'s "explicitly not building" list gets built without being asked, regardless of how natural it seems as a next step.
