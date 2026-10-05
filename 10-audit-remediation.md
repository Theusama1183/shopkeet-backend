# Shopkeet — Audit Remediation (Phase 1–24 Review Findings)

Action items from a review of `SHOPKEET_PROJECT_STATUS.md` against the live Phase 0–24 backend. Not a new feature phase — resolve these before or alongside starting Phase 25, since one of them is time-sensitive.

---

## Action 1 — Rotate the Coolify API token (do this first)

The Coolify API bearer token was shared in plaintext in `SHOPKEET-COOLIFY-MIGRATION.md` outside the project's own control plane. Treat it as compromised regardless of actual misuse likelihood — this is standard practice for any credential that leaves a controlled environment in plaintext.

**Steps:**
1. Coolify UI → generate a new API token.
2. Update every place the old one is referenced (deploy scripts, e.g. `p21-deploy.ps1`, and anywhere else it was pasted into a file or script).
3. Revoke/delete the old token (the one beginning `1|KZeej8fnR...`) so it can no longer authenticate, even if unused elsewhere.
4. Confirm the deploy pipeline still works end to end with the new token before considering this done.

---

## Action 2 — Audit tenant data from the RLS-bypass window

RLS was inert in prod (superuser connection bypass) until the 2026-09-28 fix. Before trusting that window was harmless, check what actually existed during it.

**Query to run** (adjust the cutoff timestamp to the actual fix date/time):
```sql
SELECT id, name, subdomain, created_at
FROM tenants
WHERE created_at < '2026-09-28'
  AND subdomain NOT LIKE 'p2x-smoke%'
ORDER BY created_at;
```

**If this returns only recognizable test/smoke tenants:** note that in this file or the status doc and move on.

**If it returns anything that looks like a real signup:** stop and surface it for a decision rather than deciding unilaterally whether it matters — this is a judgment call about disclosure and user impact, not an engineering one.

---

## Action 3 — Reconcile the tenant/order count

`SHOPKEET_PROJECT_STATUS.md` notes 506 tenants / 135 orders at Phase 18. Before Phase 25 work starts:
- Determine how many of these are smoke-test artifacts vs. anything else.
- If the `p2x-smoke` naming convention was adopted partway through testing, earlier test tenants may not match that prefix and could be invisible to the existing cleanup script — check for stragglers by pattern or by cross-referencing against known smoke-test runs, not just the current naming convention.

---

## Action 4 — Decide the non-Latin-1 text question (needs a product decision, not an engineering default)

Two related, already-documented caveats share one root cause:
- Invoice PDFs (`go-pdf/fpdf` core fonts, Phase 23) render non-Latin-1 characters as `?`.
- Full-text search (Phase 3) is hardcoded to `to_tsvector('english', ...)`.

**This needs an explicit answer before building more content-facing phases, not a default assumption either way:** will real merchants or customers on this platform enter Urdu-script (or other non-Latin-1) names, addresses, or product text?

- **If yes:** swap the PDF invoice generator to a Unicode/TTF-embedded font; add a per-tenant (or per-document) language config for search instead of the hardcoded English config.
- **If no:** leave both exactly as documented caveats — no code change needed, just keep them visible in the caveats list so they aren't silently forgotten.

Don't resolve this one in code without an answer first — it's cheap to decide now and expensive to half-fix later across multiple phases that touch text.

---

## Note — spec error already corrected, no action needed

`08-hardening-and-features-build-spec.md` Phase 24 incorrectly claimed `order_items.order_id` would be auto-indexed via its foreign key. Postgres only auto-indexes the *referenced* side of a foreign key, never the referencing column. This was already caught and fixed in migration `0026`. Recorded here only so the original spec's incorrect claim doesn't get copied into some future doc as if it were still true.

---

## Everything else from the review: no action needed

BOGO discounts correctly reject with `400` instead of silently computing wrong totals; the analytics conversion caveat is correctly diagnosed (carts deleted on checkout, not status-flagged) and correctly deferred rather than patched around; pgx v5 scan gotchas (date→string, aggregate casts, `ORDER BY` alias requirement) are documented for future phases to avoid re-discovering them. All genuinely solid — confirmed, not flagged.

---

## Resolution status (2026-10-02)

- **Action 2 — DONE, no action needed.** Ran the pre-`2026-09-28` tenant query. All 506 tenants
  are test artifacts: 450 `*-alpha-*`/`*-beta-*` phase-smoke pairs, 30 `auth-A-*`/`auth-B-*`
  pairs, 24 `idem-*`/`tax-*`/`cache-*` special-test, 2 smoke-other (`p17-fixcheck` etc.),
  `p-smoke` = 0. Zero real signups; no unauthorized data from the RLS-bypass window.
- **Action 3 — DONE, reconciled.** `tenants = 506`, `orders = 135` (114 pending + 21 delivered)
  as of the run; every tenant traces to a documented smoke/test run, including pre-convention
  tenants (auth pairs, idem/tax/cache) which the `p2x-smoke`-only cleanup would miss — none are
  stragglers worth deleting (66 tenants hold orders, all test origin).
- **Action 4 — RESOLVED: English-only.** Product decision (2026-10-02): merchants operate
  English/Latin-1 only; no Urdu/non-Latin-1 support planned. Both sub-caveats (PDF `?` mapping,
  hardcoded English search config) remain documented caveats permanently; see
  `SHOPKEET_PROJECT_STATUS.md` §7 caveat 5.
- **Action 1 — DONE (2026-10-02).** New token generated by operator; replaced the old token in all
  21 temp deploy/poll scripts (`p21-deploy.ps1` and friends, outside the repo), redacted the
  plaintext token in `SHOPKEET-COOLIFY-MIGRATION.md` (now points at the deploy script instead of
  embedding a credential), confirmed nothing in the repo references either token, and verified the
  pipeline end-to-end: deployment `inzulxhkrtfgygbegp2cjesf` finished, `healthz=200`. The old
  `1|KZeej8fnR...` token was **not revoked** (operator decision) — it remains valid but is no
  longer referenced or embedded anywhere.
