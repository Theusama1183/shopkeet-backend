# Shopkeet — API Reference

Every endpoint the Go API exposes, in one place. All paths are prefixed `/api/v1`. Auth column: **Public** = no token required; **Admin** = merchant JWT required, tenant-scoped by `SET LOCAL app.current_tenant`; **Customer** = guest/customer lookup, no account required for v1.

This is the contract Frontend and Backend agents build against in parallel — if it changes, update this file in the same change that changes the code (see `AGENTS.md`).

## Auth

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/auth/signup` | Public | Create a tenant + owner user in one transaction |
| POST | `/auth/login` | Public | Returns a JWT (`tenant_id`, `user_id`, `role`) |

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

## Order Tracking & Invoice PDF (Phase 23)

Merchants attach a carrier + tracking number as an order advances to `shipped`; the customer-facing order lookup surfaces it; `invoice.pdf` streams a server-generated A4 PDF whose itemized money block derives from the same columns checkout snapshots (so the total always equals `orders.total_cents`). Tracking fields are always included on the order response (`null` until set) — nothing private to scrub.

| Method | Path | Auth | Description |
|---|---|---|---|
| PATCH | `/orders/:id/status` | Admin | Existing transition body, now also accepting optional `tracking_number`, `tracking_carrier`, `tracking_url`. Persisted on **advance** transitions (not cancelled); absent fields preserved; `""` clears. |
| GET | `/orders/:id` | Customer | Response includes `tracking_number`, `tracking_carrier`, `tracking_url`. |
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
| GET | `/orders/:id` | Customer | Order lookup by id + phone/email |
| GET | `/orders` | Admin | List tenant's orders, filterable by status |
| PATCH | `/orders/:id/status` | Admin | Move order through `pending → confirmed → shipped → delivered`; `delivered` sets `payment_status=paid` |

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
