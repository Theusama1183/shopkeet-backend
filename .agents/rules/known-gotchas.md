# Rule: Known gotchas (load before writing schemas, queries, jobs, or routes)

Consolidated from bugs that were each rediscovered live (prod smoke or acceptance-as-`shopkeet_app`) rather than caught up front. These recurred; treat every item below as "will happen again unless checked."

## RLS-scoping in background jobs & ad-hoc SQL

- **Never rely on the RLS GUC alone to scope a query.** Every tenant predicate must be explicit (`tenant_id = $N`). The GUC only works when `SET LOCAL app.current_tenant` ran in the same transaction (per-request middleware does this; **scheduled jobs must set it themselves** per tenant).
- **Jobs must iterate tenants in their own RLS-scoped transaction.** `RecomputeAllTenants` runs per-tenant; the menu for a new cron job is: open a tx, `SET LOCAL`, do the work, commit — per tenant.
- **Any CTE that joins a tenant table must be tenant-filtered too.** Phase 32's `RecomputeAuto` `pairs` CTE joined `order_items` across the whole DB and only surfaced as a bug when run RLS-bypassed (superuser); the `UNIQUE(product_id, recommended_product_id, type)` collision aborted the job with 0 rows. A row count of 0 from a job that should have written rows is a symptom.
- **RLS is a defense-in-depth net, not the primary scope.** When it catches a leak, be grateful, then fix the query — Phase 32 proved it catches app-SQL mistakes in prod, but empty results (not errors) are what you get.
- **`psql` as superuser bypasses FORCE RLS** → sees all tenants' rows even with the GUC pinned. Manual checks use `-U shopkeet_app` to see what the app sees; superuser checks must filter `tenant_id` explicitly.
- **Cross-tenant pollution check** (after any RLS-bypass suspect work): scan child→parent joins for `tenant_id` mismatches + orphans (see `10-audit-remediation.md` Action 2; prod scan is 0 as of 2026-10-04). Note `product_recommendations` collides cross-tenant on its deliberately tenant-less `UNIQUE(product_id, recommended_product_id, type)` (kept tenant-less since P26; the job + RLS keep it in bounds), so its auto-recompute must be tenant-filtered in the SQL itself.
- **Recheck column plans on every new job/query** against the schema section of a 0003+ migration: `description`, `countries`, `regions`, etc. are **nullable** or `TEXT[]` with defaults — `TEXT[] NOT NULL DEFAULT '{}'` still accepts an explicit `NULL`, and a Go `nil` slice is sent as explicit NULL and trips it (`3923cfd` fix).

## pgx v5 footguns (internal/*)

- **`date`/`timestamp` → `string` scan fails** (`type "date" is not a string`): scan into `time.Time`, then format.
- **Aggregate columns come back typed wider** (`SUM`/`COUNT` → `int8`): cast `::int` in SQL or scan `int64` — mixing up is a scan error, not an overflow.
- **`ORDER BY <aggregate>` needs an output alias** — order by `AS x` defined in `SELECT`, not the raw expression.
- **"conn busy" = querying the same `pgx.Tx` while a `rows` cursor from it is still open.** Drain the `rows` (and `rows.Close()`) before the next query on that conn. P26-level multi-step handlers (e.g. auto-pick + tag eligibility) hit this; structure as: gather candidate rows → close → then decide/write.
- **`array_agg` over a LEFT JOIN yields `{NULL}` elements** and pgx can't scan to `[]string`: wrap in `COALESCE(array_agg(ct.tag) FILTER (WHERE ct.tag IS NOT NULL), '{}')`.
- **Error unwrapping:** pgconn errors need stdlib `errors.As` (not `errors.Is`) — catch `*pgconn.PgError` for unique-violation detection (23505) and check `.Code`.

## Fiber routing

- **Group middleware leaks onto routes that share the group.** Phase 31 got bitten putting `TenantMW` on a group that also served the public GET — the public route inherited a merchant-check. Use inline middleware on the privileged handlers (`r.Post("/x", TenantMW, handler)`), never group-level, unless every route under it shares the same auth.
- **`uuid.Parse` before using an id** — a non-UUID path param must 404 (not 500).

## psql / infra plumbing (everything above is "write it, verify against prod")

- **Write SQL files locally → `scp` → `docker cp` into the container → `psql -f`.** Never nest SQL over `ssh`; quoting mangles it.
- **`psql -tAc -f` is invalid** (`-c` swallows `-f`): use `-q -tA -f`.
- **`psql -tAc 'INSERT … RETURNING id'` captures the status tag too** — the `TID` ends up polluted unless `-q` is added.
- **PowerShell writes a BOM with `Set-Content -Encoding utf8`** (PS 5.1) → "Invalid JSON." Use `[System.IO.File]::WriteAllText(…, new UTF8Encoding($false))`.
- **`+` in a URL query is a space** — phone-number lookups (invoice PDF, order lookup) must `url.QueryEscape` the param, or the record 404s.
- **CRLF**: after `scp` of a `.sh`, `sed -i 's/\r$//'`.
- **Every deploy is manual** — `p21-deploy.ps1` (POST `/api/v1/deploy?force=true`). Auto-deploy is flagged in Coolify settings but **no Git webhook is connected** (`source_id=0`); pushes to `main` do not deploy. After a release: push, run `p21-deploy.ps1`, poll to `finished`, then `curl https://api.shopkeet.com/healthz` (expect `200 {"status":"ok"}`).

## Security hygiene at the file level

- Never embed the Coolify API token (or R2/Resend secrets) in repo files; deploy scripts live outside the repo (temp dir) and env is set via Coolify env vars. `SHOPKEET-COOLIFY-MIGRATION.md` once carried the token in plaintext — that failed an audit review; keep it out.