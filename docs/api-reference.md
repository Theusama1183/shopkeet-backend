# Shopkeet — API Reference

Every endpoint the Go API exposes, in one place. All paths are prefixed `/api/v1`. Auth column: **Public** = no token required; **Admin** = merchant JWT required, tenant-scoped by `SET LOCAL app.current_tenant`; **Customer** = guest/customer lookup, no account required for v1.

This is the contract Frontend and Backend agents build against in parallel — if it changes, update this file in the same change that changes the code (see `AGENTS.md`).

## Auth

The merchant is the **account** (email + password); a store is a tenant the account owns or staffs. A merchant on a paid plan can run several stores, so login authenticates the account and never asks which store — it returns every store the account can open.

When a mail provider is configured (SMTP or Resend), signup and login are gated by an **email OTP**: the password is only half the check, and neither endpoint returns a token until `/auth/otp/verify` accepts the 6-digit code. Without a mail provider the endpoints answer directly (a box that cannot send mail must not lock its users out), and the verification endpoints return `503 verification_unavailable`.

A correct code also marks the browser as a **trusted device**: `/auth/otp/verify` returns `device_token` (90 days, but bound to the account's current password hash, so a password change invalidates it), which the web app keeps as its own httpOnly cookie and forwards as the `X-Otp-Bypass` header on `/auth/login`. Login accepts the password and skips the code for a valid ticket — verification is a one-time step per browser, not per login.

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/auth/signup` | Public | Create an account, a provisional store and its owner membership in one transaction. Body `{email, password}` — no store name, no subdomain. **Verification on:** `201 {mode:"otp_required", email, method:"email"}`, no token — the code mails a 6-digit OTP (10 min). **Off:** `{token, user, store, onboarding_completed:false, expires}`, session flagged for the onboarding wizard |
| POST | `/auth/login` | Public | Body `{email, password}`. Optional `X-Otp-Bypass` header: a valid trusted-device ticket for this account skips the code step below. **Verification on, no ticket:** `200 {mode:"otp_required", email, method:"email"}` after the password checks out — no token yet. **Ticket accepted / verification off:** returns `{user, stores[]}`. **One store:** also `token` + `store` + `onboarding_completed`. **Several stores:** no tenant JWT — only a 10-minute `store_pick_token` |
| POST | `/auth/otp/send` | Public | Re-issues the code for a login/signup in flight. Body `{email, contactMethod:"email"}`. `200 {sent:true}`; `400 verification_expired` when there is no live code or the original issue is older than 30 min (same error whether the row never existed — anti-enumeration); `400 sms_unavailable` for any non-email channel. Refreshes the live code in place: new code, same 10-minute window, failed-guess count carried over |
| POST | `/auth/otp/verify` | Public | Body `{email, code}`. Correct code → same payload login would have given: one store → `{token, store, onboarding_completed, user, expires}`; several → `{store_pick_token, stores[]}`. Both also carry `device_token` — the trusted-device ticket the web app stores for future logins. Wrong code → `400 invalid_code` (5 per code burns it: `400 too_many_attempts`); 20 failed guesses per email per hour → `429 too_many_attempts` |
| POST | `/auth/forgot-password` | Public | Body `{email}`. **Always** `200 {sent:true}`, whatever the address — the endpoint is not an account oracle. For a known address, mails a single-use link (`https://auth.<base>/reset-password?token=…`, valid 1 h, stored as SHA-256) and invalidates any older link |
| POST | `/auth/reset-password` | Public | Body `{token, password}` (min 8 chars). `400 invalid_token` for a bad/expired/already-used link. On success rewrites the password on the account **and** every store membership, kills any in-flight OTP, and returns the same session payload as login/verify |
| GET | `/auth/stores` | store_pick token or merchant JWT | List the stores an authenticated account can open: `{stores[]}` |
| POST | `/auth/select-store` | store_pick token or merchant JWT | Exchange a store pick for a tenant session. Body `{tenant_id}`; membership is re-checked, so a ticket alone grants nothing. Returns `{token, user, store, onboarding_completed, expires}` |

A merchant token may be used for `/auth/stores` and `/auth/select-store`, which is how a signed-in merchant switches stores without logging in again.

Codes and reset tokens live in `auth_verification_codes` (migration 0035) as SHA-256 hashes only, one live row per account+purpose, pruned after a day.

### Store onboarding

`tenants.onboarding_completed` is `false` only for a store created by signup. While it is false the admin surface routes the merchant through the one-time wizard; after completion it is `true` forever.

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/tenant/onboarding/complete` | Admin | Closes the wizard: flips `onboarding_completed`, returns a refreshed `token` plus the settings, so the session stops asking for the wizard. Idempotent |

`GET/PATCH /tenant/settings` also carries `onboarding_completed`, and `PATCH /tenant/settings` now accepts `subdomain` — that is where a merchant claims their store link (normalised to lowercase; `409` when taken).

## Media (Cloudflare R2)

File bytes never pass through the Go API — the browser uploads directly to R2 using a short-lived presigned URL the API issues.

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/media/upload-url` | Admin | Request a presigned R2 PUT URL for a given filename/content-type; returns `{upload_url, r2_key, expires_in}` |
| POST | `/media` | Admin | Confirm an upload finished; records `r2_key`, `content_type`, `size_bytes`, `alt_text` in `media_assets`, returns the asset (with public `url`) |
| GET | `/media` | Admin | List the tenant's media library |
| DELETE | `/media/:id` | Admin | Delete an asset from R2 and the database |

## Catalog

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/products` | Public | List `status=active` products for the resolved tenant |
| GET | `/products/:id` | Public (active) / Admin | Fetch one product, including its ordered `product_images` |
| POST | `/products` | Admin | Create a product |
| PATCH | `/products/:id` | Admin | Update a product |
| DELETE | `/products/:id` | Admin | Remove a product |
| POST | `/products/:id/images` | Admin | Attach a `media_asset_id` to a product with a sort order |
| DELETE | `/products/:id/images/:imageId` | Admin | Remove an image from a product |
| GET | `/categories` | Public | List categories for the resolved tenant |

## Cart

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/cart` | Customer | Fetch the current cart (by session cookie) |
| POST | `/cart` | Customer | Create a cart / add an item |
| POST | `/cart/bundle` | Customer | Add a bundle (Phase 25): `{bundle_id, quantity?}` for fixed, `{bundle_id, selections:[{product_id, quantity?}]}` for mix-and-match. Returns the cart already priced as a bundle. |
| PATCH | `/cart/items/:id` | Customer | Change quantity |
| DELETE | `/cart/items/:id` | Customer | Remove an item |

## Product Bundles & Quantity Breaks (Phase 25)

Merchants price a **fixed** bundle at one flat price (`bundle_price_cents`) or compose a **mix-and-match** bundle that discounts a customer's whole pick (`discount_percent` off the summed components) — exactly one pricing field per bundle. `type` defaults to `fixed`. A fixed bundle expands in the cart into one line per component variant (each tagged `bundle_id`), and the cart/checkout charge the **bundle** price, never the sum of its variants; checkout decrements each component's own stock and `order_items` snapshot each component's **real** unit price with its `bundle_id`. Quantity breaks give a plain line the best `discount_percent` once its quantity clears `min_quantity`; breaks never stack on bundle lines. Bundles in the cart must stay `active` — draft/archived → `409`, unknown → `404`, deleted → component sum fallback.

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/bundles` | Admin | Create: `{name, type?, bundle_price_cents?\|discount_percent?, status?, items:[{product_id, quantity?}]}`. Mixed shapes / cross-tenant / inactive items → `400`. |
| GET | `/bundles` · `/bundles/:id` | Admin / Public (single `PublicOrAdminMW` route) | List/fetch. Merchant sees all statuses + `status` field (+ optional `?status=` filter); Public sees active only, no `status` leak — one route since `9165c9a` (a split public + admin `GET /` was shadowed by Fiber). RLS scopes tenant. |
| PATCH | `/bundles/:id` | Admin | Partial merge; `items` replaces wholesale; a pricing field flips the mode (re-validated merged). |
| DELETE | `/bundles/:id` | Admin | Delete (items cascade); `409` while referenced by a cart/order. |
| GET | `/bundles` | Public | Active bundles only, no `status` leak. *(Now the same single `PublicOrAdminMW` route as above — removed as a separate route in `9165c9a`.)* |
| POST | `/products/:id/quantity-breaks` | Admin | `{min_quantity, discount_percent}`; duplicate `(product, min_quantity)` → `409`. |
| GET | `/products/:id/quantity-breaks` | Admin | List a product's breaks. |
| PATCH · DELETE | `/products/:id/quantity-breaks/:bid` | Admin | Update / delete a break. |

## Upsell, Cross-sell & Post-Purchase Recommendations (Phase 26)

Merchants curate the storefront's **"you may also like"** rail per product (`product_recommendations`, migration `0028`), and a pending COD order gains a **post-purchase add-item** endpoint so the confirmation screen can upsell before fulfilment starts. The rail is one route served both to the public storefront (active picks only, no status leak) and the merchant (every pick incl. archived + `status`) — same `PublicOrAdminMW` dual-view pattern catalog's `GET /products/:id` uses. `type` distinguishes merchant-curated `manual` rows from `auto` (reserved for the Phase 32 scheduled job).

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/products/:id/recommendations` | Public / Admin (single route) | The rail. Public: recommended product must be **active**, no status leak. Admin: all picks incl. archived with `status`. Unknown product → `404` (both views). Sorted `type, sort_order, name`. |
| POST | `/products/:id/recommendations` | Admin | `{recommended_product_id, type?, sort_order?}`. `type` defaults `manual` (`manual`/`auto` only); self-recommend → `400`; either product inactive → `400`; duplicate → `409`; `sort_order < 0` → `400`. Idempotency-guarded; `201`. |
| DELETE | `/products/:id/recommendations/:rid` | Admin | Remove a pick (scoped to the product); bogus id → `404`. |
| POST | `/orders/:id/add-item` | Customer (JWT or guest session + `customer_phone`) | Post-purchase upsell on a **pending** COD order: `{customer_phone, variant_id, quantity}` (1–99). Recorded phone must match or the order `404`s; locked order `FOR UPDATE`, `confirmed`+ → `409 "order can no longer be modified"`. Re-runs the checkout stock guard (FOR UPDATE variant+product, preorder semantics) → `409 "insufficient stock"` / inactive product. Snapshots the variant's current `unit_price_cents`, prices the new line with the checkout pricer (quantity breaks / bundles), bumps `total_cents` by the delta (discount/gift/shipping/tax snapshots are immutable), decrements inventory, refreshes product aggregates, invalidates the Redis cache. |

## Storefront Analytics (Phase 24)

Three merchant-only aggregation endpoints over existing data — no new tables. "Sales" = non-cancelled orders (COD revenue counts at placement). All run inside the tenant tx, so RLS scopes every aggregate. Period = `7d|30d|90d`, default `30d`; anything else → `400`. Backed by migration-0026 indexes (`orders(tenant_id, created_at DESC)`, `order_items(order_id)`, `carts(tenant_id, created_at)`).

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/analytics/sales?period=` | Admin | `{period, currency, totals:{revenue_cents, order_count}, buckets:[{date, revenue_cents, order_count}]}` — total + per-UTC-day buckets of non-cancelled orders. |
| GET | `/analytics/top-products?period=&metric=quantity\|revenue&limit=` | Admin | `{period, metric, currency, items:[{product_id, product_name, quantity, revenue_cents}]}` — best sellers grouped by product (variant sales roll up), ranked by `metric` (default `quantity`), `limit` default 10 max 50. Cancelled orders contribute nothing. |
| GET | `/analytics/conversion?period=` | Admin | `{period, carts_created, orders_placed, conversion_rate}` — carts present vs. orders placed (incl. cancelled) in the window; `conversion_rate = orders_placed / carts_created`, 4dp. |

## Record Search (Admin command palette)

One merchant endpoint powering the admin search/command palette. It queries products, orders and customers in a single round trip, grouped the way the palette renders them. Each table matches on a GENERATED `search_vector` tsvector column (products: migration 0006; orders/customers: 0036) behind a GIN index, using the same `plainto_tsquery('english', q)` predicate the catalog's product list uses. Groups are always present (empty arrays when nothing matches). Merchant-only; RLS scopes every group to the caller's tenant.

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/search?q=` | Admin | `{products:[{id, name, status}], orders:[{id, customer_name, status, total_cents, currency}], customers:[{id, email, phone}]}` — newest-first matches, max 8 per group. `q` required, ≤ 100 chars; empty/over-long → `400`. |

## Order Tracking & Invoice PDF (Phase 23)

Merchants attach a carrier + tracking number as an order advances to `shipped`; the customer-facing order lookup surfaces it; `invoice.pdf` streams a server-generated A4 PDF whose itemized money block derives from the same columns checkout snapshots (so the total always equals `orders.total_cents`). Tracking fields are always included on the order response (`null` until set) — nothing private to scrub.

| Method | Path | Auth | Description |
|---|---|---|---|
| PATCH | `/orders/:id/status` | Admin | Existing transition body, now also accepting optional `tracking_number`, `tracking_carrier`, `tracking_url`. Persisted on **advance** transitions (not cancelled); absent fields preserved; `""` clears. |
| GET | `/orders/:id` | Admin **or** Customer | Order detail. Admin (Phase B admin pages): any order in the tenant by id, `internal_note` included. Customer: id + `?phone=`, optional `?email=` (verified) → `404` on mismatch. |
| GET | `/orders/:id/invoice.pdf` | Admin **or** Customer (`?phone=` lookup) | `application/pdf` body starting `%PDF-`. Admin = by id only. Customer path requires the matching `?phone=` → `404` on mismatch. |

## Wishlist (Phase 22)

A signed-in customer's saved products, one row per `(customer_id, product_id)`. Duplicate adds answer `409`. Products of any status can be saved; responses include the live product's `status` so the storefront can grey out drafted/archived items.

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/customers/me/wishlist` | Customer | `{items}` newest-first; each `{id, product_id, product_name, product_slug, price_cents, currency, status, created_at}`. |
| POST | `/customers/me/wishlist` | Customer | Body `{product_id}`. `201` with the created item; `409` duplicate; `404` unknown product; `400` missing `product_id`. |
| POST | `/customers/me/wishlist/:productId` | Customer | Route-param form of the add (same responses). |
| DELETE | `/customers/me/wishlist/:productId` | Customer | `204` removed; `404` if the product isn't on the list. |

## Advanced & Automatic Discounts (Phase 21)

Discounts can be **automatic** (`requires_code=false`, `code` null) — a store-wide promo or "free shipping over $X" that applies at checkout with no code. When a code is also entered, exactly one of them applies: **whichever saves more** (never stacking), and only the winner's `times_used` is bumped.

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/discounts` | Admin | Create a discount. New fields: `applies_to` (`order` default | `shipping`; `product`/BOGO reserved → 400), `requires_code` (default `true`; set `false` for automatic — no `code` allowed/needed), plus reserved `buy_quantity`/`get_quantity` (any non-null → 400). |
| GET | `/discounts` · `/discounts/:id` | Admin | List/fetch, now including `applies_to`, `requires_code`, `code` (`null` for automatic), `buy_quantity`/`get_quantity` (`null`). |
| PATCH | `/discounts/:id` | Admin | Update any subset; absent fields preserved. |

Checkout picks automatic discounts (`status='active'`, `requires_code=false`, inside window, under `usage_limit`) under `FOR UPDATE` locks ordered by `id`, compares each against the goods subtotal threshold, and applies the best single value (`order` scope discounts goods; `shipping` scope discounts shipping only, tax base untouched). Ties resolve to the lowest `id`. Codes stay on the `/cart` `/cart/discount` path and are never resolvable as a code when `requires_code=false`.

## Loyalty & Referrals (Phase 20)

Merchants tune the program via `loyalty_points_per_currency_unit` (points earned per 1 currency unit on a delivered order; 0 = disabled) and `loyalty_redemption_rate` (points per 1 currency unit of redeemed discount), both on `GET/PATCH /tenant/settings`.

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/customers/me/loyalty` | Customer | `{balance, ledger}` — running points + recent history (`{id, points, reason, order_id?, created_at}`). Credits land when an order reaches `delivered` and is linked to the account. |
| GET | `/customers/me/referral` | Customer | `{code, value_cents}` — the caller's unique `fixed_amount` referral code (value 500), lazily created once per customer. Use shares it at checkout; the referrer earns points when the referred order is delivered. |
| POST | `/loyalty/redeem` | Customer | Body `{points}`. Converts points into a one-time discount code applied to the current cart. Returns `{code, discount_cents, points, balance}`. 400 if points exceed the balance, redemption is disabled (rate ≤ 0), or the points don't reach one full currency unit. Idempotency-guarded. |

## Checkout & Orders

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/checkout` | Customer | Validates stock, creates the order (`payment_method=cod`), decrements inventory, clears cart |
| GET | `/orders/:id` | Admin **or** Customer | Order detail. Admin: by id, `internal_note` included. Customer: id + `?phone=` (verifed), optional `?email=` |
| GET | `/orders` | Admin | List tenant's orders, each filter optional and combinable: `?status=`, `?payment_status=`, `?source=` (the Drafts view uses `source=draft`) |
| PATCH | `/orders/:id/status` | Admin | Move order through `pending → confirmed → shipped → delivered`; `delivered` sets `payment_status=paid` |
| GET | `/carts/abandoned` | Admin | Unconverted checkouts — carts with a captured `customer_email` that still have items (checkout deletes the cart, so a surviving row with lines produced no order). `{carts:[{id, customer_email, created_at, last_activity_at, recovery_sent_at, item_count, total_cents}]}` |

## Content & Page Builder

**Posts** — one-off content (`page`, `blog_post`, ...):

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/posts?post_type=page&route=/` | Public | Fetch one published post by type + route |
| GET | `/posts?post_type=blog_post` | Public | List published posts of a type |
| POST | `/posts` | Admin | Create a post |
| PATCH | `/posts/:id` | Admin | Update a post (layout, SEO fields, status) |
| DELETE | `/posts/:id` | Admin | Remove a post |

**Templates** — rendering rules applied across many instances (`product`, `product_archive`, `cart`, `404`, `order_confirmation`, ...):

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/templates/:template_type` | Public | Fetch the tenant's active template (`scope=default`) so the storefront can render it |
| PUT | `/templates/:template_type` | Admin | Create or update the tenant's template for that type |

**Sections** — global chrome not tied to one route (`header`, `footer`, `announcement_bar`, `popup`):

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/sections?section_type=header` | Public | Fetch the published header/footer for rendering |
| GET | `/sections?section_type=popup` | Public | List active popups; storefront evaluates `placement_rules` client-side |
| POST | `/sections` | Admin | Create a section (mainly popups — header/footer are typically one row each, updated via PATCH) |
| PATCH | `/sections/:id` | Admin | Update a section's layout, placement rules, or status |
| DELETE | `/sections/:id` | Admin | Remove a section |

**Redirects:**

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/redirects` | Admin | List the tenant's redirects |
| POST | `/redirects` | Admin | Create a redirect (e.g. after renaming a post's `route`) |
| DELETE | `/redirects/:id` | Admin | Remove a redirect |
| GET | `/redirects/lookup?path=/old-page` | Public | Used by Next.js middleware before falling through to the `404` template |

## Platform

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/healthz` | Public | Liveness check |
| GET | `/metrics` | Internal | Prometheus scrape endpoint (not exposed publicly) |

## Error shape (every endpoint)

```json
{ "error": { "code": "product_not_found", "message": "No product with that id in this store." } }
```

## Not built yet (deferred — see `docs/04-agent-build-spec.md`'s forward-compatibility section)

- `/webhooks/*` — subscriber endpoints for a future developer marketplace
- A public, versioned developer API (likely GraphQL) separate from this internal REST contract
- Any online payment method beyond Cash on Delivery
- Post/template revision history (undo to a previous saved version)
- Blog archive, search-results, and announcement-bar template types (schema supports them; not built in the v1 phase plan)
