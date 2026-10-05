# Shopkeet — Frontend Foundation (Stack, Design Discipline, Component Library)

This is Phase 0–1 of `apps/web`, the first installment of the frontend build. Everything downstream (auth screens, admin catalog/orders, the storefront, Puck integration) gets specced in follow-up files once this foundation is live — building screens before the component library and design discipline exist is how a frontend ends up looking like twenty different apps stitched together.

Read `docs/05-frontend-agent-spec.md` first — it defines *how this should look*, not just what to build. This file is the concrete setup that makes those rules actually enforceable: real tokens, real reusable components, no screen inventing its own button.

---

## Phase 0 — Core Stack & Design Tokens

**Library choices — decided, not open questions:**

| Layer | Choice | Why |
|---|---|---|
| Framework | Next.js (App Router) | Already decided — ISR/SSR mix the storefront needs. |
| Styling | Tailwind CSS | Utility-first, pairs directly with CSS-variable design tokens. |
| Component primitives | **shadcn/ui** — copied into the repo, never used as an unmodified npm dependency | Radix-based: real accessible behavior (focus management, keyboard nav, ARIA) for free. But shadcn's *default theme* is one of the most common AI-generated-app tells on the internet right now — every primitive gets re-themed against Shopkeet's own tokens before it ships. Using shadcn for behavior and ignoring its default look is the point. |
| Icons | lucide-react | Standard pairing with shadcn, large consistent set. |
| Forms | react-hook-form + zod | Type-safe validation; Zod field errors map cleanly onto the Go API's structured error shape. |
| Data fetching | Server Components for initial reads (direct server-side fetch to the Go API — no client waterfall); **TanStack Query** for client-side mutations and anything needing optimistic updates or refetch (cart, admin table actions) | Don't fetch client-side what can be fetched server-side; don't hand-roll cache invalidation for the parts that do need to be client-side. |
| Tables | **TanStack Table** (headless) wrapped in Shopkeet's own `DataTable` component | Sorting, filtering, pagination, row selection — the backbone of every admin list screen. |
| Toasts | Sonner | Clean, accessible, minimal footprint. |
| Charts | Recharts | For Phase 24's analytics endpoints. |
| Dates | date-fns | Lighter than moment.js, no reason for anything heavier. |
| Animation | `tailwindcss-animate` utility classes only — **no framer-motion by default** | Matches `05-frontend-agent-spec.md`: motion is rare and purposeful, not ambient. Add framer-motion only for one specific, deliberate moment that genuinely needs it — never as a default dependency. |
| API client | A hand-written typed `lib/api.ts` wrapper per endpoint in `docs/api-reference.md` | The API isn't formally OpenAPI-specced, so a codegen tool is more ceremony than value at this size. One file, typed request/response per route, normalizes error handling (see Phase 1). |

**Design tokens — set up before any component exists:**

- Define real tokens as CSS variables (`:root` + dark-mode override), per `05-frontend-agent-spec.md`'s technical conventions: 4–6 named colors, a type scale, a spacing scale. Not Tailwind's bundled defaults left untouched — that's exactly the generic-look failure mode the design spec warns about.
- Two token sets from day one: the **admin** surface's own palette (functional, Stripe/Linear-register) and a **storefront** palette that's *configurable per tenant* (reads from tenant settings at render time), since storefronts need to look different from merchant to merchant.

**Acceptance:** a blank Next.js app builds and deploys with Tailwind + the token set wired in; running the app with no tenant-specific theme falls back to sane defaults; switching a test tenant's brand color actually changes the storefront's rendered palette without a code change.

---

## Phase 1 — Reusable Component Library

Build these once, in isolation, before any real screen. A simple internal route (`/dev/components`, not shipped to production, gated behind a dev-only flag) that renders every component in every state is enough for visual QA at this stage — don't stand up Storybook for a small team's first component pass; that's tooling overhead disproportionate to what it buys right now.

**Tier 1 — Primitives** (shadcn-sourced, re-themed): `Button` (primary/secondary/destructive/ghost variants), `Input`, `Textarea`, `Select`, `Checkbox`, `Radio`, `Switch`, `Badge`, `Avatar`, `Tooltip`, `Separator`.

**Tier 2 — Layout & navigation:** `Card`, `Tabs`, `Dialog` (modal), `Sheet` (drawer), `DropdownMenu`, `Breadcrumbs`, `Pagination`, `Sidebar` (admin nav shell).

**Tier 3 — Feedback & state** (the error/loading ask, made concrete):
- `Skeleton` — used for every loading list/table/card. **Default to skeletons over spinners** for primary content: a skeleton communicates layout before data arrives and reads as faster, more deliberate — closer to the Shopify/Linear/Stripe bar than a generic spinner.
- `Spinner` — reserved for small inline affordances (a button's own pending state), not full-page loading.
- `EmptyState` — icon + message + one primary action. Every list screen's empty case uses this; "No data" with nothing else to do is not an acceptable empty state anywhere in the admin.
- `ErrorState` — for when an entire section fails to load: message + a retry action, never a dead screen.
- `Alert` — inline info/warning/error banners (distinct from toasts, which are transient).
- `ConfirmDialog` — built on `Dialog`; every destructive or payment-affecting admin action (cancel order, delete product, void a commission) routes through this, per `05-frontend-agent-spec.md`'s admin rule.

**Tier 4 — Admin composites:**
- `DataTable` — wraps TanStack Table: sorting, filtering, pagination, row-selection checkboxes, and a sticky bulk-action bar that appears once rows are selected (archive/delete/export). This is the primary UI for products, orders, customers — not cards, per the existing admin design rule.
- `StatCard` — a single analytics number with a label and optional trend, for the Phase 24 dashboard.
- `PageHeader` — breadcrumb + title + primary action button, used identically across every admin screen so navigation feels like one coherent product, not twenty.
- `StatusBadge` — order/payment/affiliate status pills with one consistent color mapping defined once, reused everywhere a status appears.
- `FilterBar` — the standard filter/search row above a `DataTable`.

**Tier 5 — Commerce-specific:** `ProductCard`, `PriceDisplay` (cents → formatted currency; handles a variant price range as "from $X"), `VariantSelector`, `QuantityStepper`, `CartLineItem`, `MediaPicker` (browses/uploads against the R2 media endpoints from Phase 2).

**Acceptance:** every component in Tiers 1–4 renders correctly in the `/dev/components` gallery in both light and dark token sets; `DataTable` correctly sorts/filters/paginates against a mock dataset; `ConfirmDialog` blocks a destructive action until confirmed.

---

## Error & loading: the actual wiring, not just the components

- **`lib/api.ts`** normalizes every response: a success returns typed data; a failure throws a typed `ApiError` carrying the backend's `{code, message}`. Nothing downstream parses raw fetch responses by hand.
- **Validation errors** (`400` with field-level detail) map onto `react-hook-form`'s field error state — shown inline, next to the field, not as a toast.
- **Everything else** (`404`, `409`, `500`, etc.) surfaces as a toast by default; a failed full-section load (e.g., the orders table itself can't load) renders `ErrorState` instead of an empty table.
- **`429` responses** (Phase 14 rate limiting) get their own toast copy, surfacing the `Retry-After` value — "Too many attempts, try again in 2 minutes," not a generic error message.
- Next.js route conventions: `not-found.tsx`, a per-route `error.tsx`, and a root `global-error.tsx` — all three, not just the root one.

---

## What comes next (not in this file)

Once this foundation is live: auth screens (merchant login/signup), the admin shell (sidebar + `PageHeader` wired together), catalog/orders admin screens (`DataTable` doing real work against live endpoints), then the storefront and Puck integration. Each gets its own build-spec file against this foundation, the same way Phase 15 onward built against Phase 0–14's backend foundation — not speced all at once here.
