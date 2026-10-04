# Rule: Go coding conventions

Applies to: all code under `apps/api/`. Load this rule for any backend task.

- Use the standard Go project layout: `cmd/api/main.go`, `internal/<module>/`.
- One package per module: `tenants`, `media`, `catalog`, `cart`, `orders`, `payments`, `content`, `platform`. Modules do not reach into each other's internals — only through a small exported interface.
- Every exported function that touches the database takes `context.Context` as its first argument and threads `tenant_id` through it. Never store tenant state in a package-level global.
- Wrap errors with context — `fmt.Errorf("creating product: %w", err)` — never a bare error return with no context of what failed.
- Every package has table-driven tests. A handler added without a corresponding test is not complete, regardless of how simple it looks.
- Migrations are one file per change, sequentially numbered (`0001_init.sql`, `0002_products.sql`, ...). Never edit a migration that has already been applied — write a new one that changes what's needed.
- API responses use a consistent JSON error shape: `{"error": {"code": "...", "message": "..."}}`. No raw stack traces or unhandled panics reach the client — every handler is covered by recover middleware.
- Commits and PRs stay scoped to one phase's work, in small reviewable units — not one large commit at the end of a phase.
