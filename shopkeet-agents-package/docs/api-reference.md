# Shopkeet — API Reference

Every endpoint the Go API exposes, in one place. All paths are prefixed `/api/v1`. Auth column: **Public** = no token required (tenant resolved from `X-Tenant-ID`); **Admin** = merchant JWT required, tenant-scoped by `SET LOCAL app.current_tenant`; **Customer** = customer-scoped JWT required (guest endpoints resolve the session from cookie/`X-Customer-Session`). Merchant and customer JWTs are distinct scopes — an admin route refuses a customer token (403) and vice versa.

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
| GET | `/products/:id` | Public (active) / Admin | Fetch one product, including its ordered `product_images`, `product_options` (with values) and `product_variants` (each with `option_values` links, `sku`, `price_cents`, `inventory_count`, `weight_grams`, `status`) |
| POST | `/products` | Admin | Create a product; **auto-creates one "Default" variant** carrying the flat price/stock (every product always keeps ≥1 variant) |
| PATCH | `/products/:id` | Admin | Update a product; when the product has exactly one variant, `price_cents`/`inventory_count` cascade to it |
| DELETE | `/products/:id` | Admin | Remove a product (and its options/variants); 409 if any variant is referenced by carts/orders |
| POST | `/products/:id/images` | Admin | Attach a `media_asset_id` to a product with a sort order |
| DELETE | `/products/:id/images/:imageId` | Admin | Remove an image from a product |
| POST | `/products/:id/options` | Admin | Create a product option (e.g. "Size") with its `values` in one call |
| POST | `/products/:id/variants` | Admin | Create a variant: `option_value_ids` (must belong to this product's options), `sku`, `price_cents`, `inventory_count`, `weight_grams`, `status`. Recomputed product price = MIN active variant price, product stock = SUM active variant stock |
| PATCH | `/products/:id/variants/:variantId` | Admin | Update a variant (`sku` `null` clears it); `option_value_ids` replaces the links when provided |
| DELETE | `/products/:id/variants/:variantId` | Admin | Delete a variant; **cannot delete the last remaining variant** (400), 409 if referenced by carts/orders |
| GET | `/categories` | Public | List categories for the resolved tenant |

**Cart & orders reference variants, not products directly:** `cart_items` line = `(cart_id, variant_id)` (one line per variant, adds merge quantity), `order_items` carry `variant_id` in addition to `product_id` and snapshot `unit_price_cents`. Product totals on a multi-variant product begin at the cheapest active variant.

## Cart

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/cart` | Customer | Fetch the current cart (by session cookie) |
| POST | `/cart` | Customer | Create a cart / add an item — body `{variant_id, quantity}` (variant of an active product); same variant merges quantity into one line |
| PATCH | `/cart/items/:id` | Customer | Change quantity |
| DELETE | `/cart/items/:id` | Customer | Remove an item |
| POST | `/cart/discount` | Customer | Apply a discount code to the current cart — body `{code}`. Validated at apply time (`invalid_request`/`not_found` on failure); the code is recorded on the cart and re-validated at checkout |

**Cart JSON** includes `discount_code` (the applied code, or empty) and `discount_cents` (the predicted discount against the current subtotal — the cart's best-effort estimate; checkout is authoritative).

## Checkout & Orders

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/checkout` | Customer | Requires structured shipping address (`shipping_address_line1`, `shipping_city`, `shipping_country`) plus `shipping_rate_id`; resolves the rate against the destination (mismatch → 400), applies `free_over_cents` (cost waived at threshold), snapshots method + cost into the order, validates stock per variant, creates the order (`payment_method=cod`), decrements each variant's `inventory_count`, refreshes the cached product totals, clears cart. When the caller presents a customer JWT the order is linked via `customer_id`; a guest checkout leaves it NULL |
| GET | `/orders/:id` | Customer | Order lookup by id + phone/email |
| GET | `/orders` | Admin | List tenant's orders, filterable by status |
| PATCH | `/orders/:id/status` | Admin | Move order through `pending → confirmed → shipped → delivered`; `delivered` sets `payment_status=paid` |
| PATCH | `/orders/:id/note` | Admin | Set/update the merchant-only internal note on an order (never shown to the customer) |

**Shipping:** see the next table. Order JSON includes `shipping_address_line{1,2}`, `shipping_city`, `shipping_state`, `shipping_postal_code`, `shipping_country`, `shipping_method` (rate name snapshot) and `shipping_cost_cents`. The legacy `shipping_address` field is no longer written (empty for new orders).

**Discounts:** if the cart has a `discount_code`, checkout re-validates it inside the order transaction (`FOR UPDATE` on the code row) and increments `times_used`; a code that expired, was disabled, or hit its usage limit since it was applied → 400 and the order is not created. Order JSON includes `discount_code` (or empty) and `discount_cents` (what was actually applied).

**Tax (Phase 13):** `tax_cents = subtotal_cents × tenants.tax_rate_percent / 100`, added into `total_cents`. Order JSON includes `tax_cents` and (for admin responses) `internal_note` — the merchant's private note never appears in customer-facing responses.

**Customer accounts:** order JSON includes `customer_id` — the linked `customers` row when checkout ran with a customer JWT, else `null`.

**Total formula:** `total_cents = subtotal − discount_cents + shipping_cost_cents + tax_cents`. Percentage discount = `subtotal × value_percent / 100`; fixed amount = `min(value_cents, subtotal)`.

## Shipping

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/shipping/rates?country=&state=` | Public | Every rate of every zone covering the destination (country in the zone's `countries`; a region-restricted zone also requires the state to be in its `regions` and is skipped without one). `free_over_cents` included — the options a checkout form presents |
| POST | `/shipping/zones` | Admin | Create a zone: `{name, countries[], regions[]}`. `regions` (state codes) restricts the zone; empty means nationwide |
| PATCH | `/shipping/zones/:id` | Admin | Update a zone. Changing the type (restricted ↔ unrestricted) while rates exist → 409 |
| DELETE | `/shipping/zones/:id` | Admin | Delete a zone; 409 while any rate still references it (delete the rates first) |
| POST | `/shipping/rates` | Admin | Add a rate to a zone: `{zone_id, name, rate_cents, free_over_cents?, sort_order}` |
| PATCH | `/shipping/rates/:id` | Admin | Update a rate (`name`, `rate_cents`, `free_over_cents`, `sort_order`) |
| DELETE | `/shipping/rates/:id` | Admin | Remove a rate |

**Checkout integration:** `POST /checkout` requires a `shipping_rate_id` that resolves to the destination (country/state), else 400. The applied cost — `rate_cents`, or `0` when the subtotal is `>= free_over_cents` — is added to `total_cents` and snapshotted. Later rate/zone edits never alter past orders.

## Discounts

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/discounts` | Admin | List the tenant's discount codes |
| POST | `/discounts` | Admin | Create a code — body `{code, type, value_percent?, value_cents?, min_subtotal_cents?, starts_at?, ends_at?, usage_limit?, status?}`. `type` is `percentage` (requires `value_percent` 1–100) or `fixed_amount` (requires `value_cents` > 0); dates are RFC3339; `status` defaults to `active`; duplicate `code` → 409 |
| GET | `/discounts/:id` | Admin | Fetch one code |
| PATCH | `/discounts/:id` | Admin | Update a code — any of the create fields except that switching type requires its own value (`type`-to-`fixed_amount` without `value_cents` → 400). `code` is normalized to uppercase; blank required fields → 400 |
| DELETE | `/discounts/:id` | Admin | Remove a code → 204 |

**Validity rules (checked at both apply and checkout):** the code must exist (`not_found`), be `active`, be inside `starts_at`/`ends_at`, meet `min_subtotal_cents`, and not exceed `usage_limit`. A code on the cart whose validity lapses before checkout is rejected **at checkout** with 400 — the order is not created. Customer-facing errors keep the standard `{"error":{code,message}}` shape.

## Customer Accounts

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/customers/signup` | Public | Create a customer — body `{email, phone, password}` (all required; email is unique per tenant, `409` on duplicate). Returns `{token, expires, customer}` with a **customer-scoped JWT** (`{tenant_id, customer_id, scope:"customer"}`) |
| POST | `/customers/login` | Public | `{email, password}` → `{token, expires, customer}`; wrong credentials → 401 |
| GET | `/customers/me` | Customer | The customer's `{id, email, phone, addresses[]}` profile (addresses with `is_default`) |
| GET | `/customers/me/orders` | Customer | The customer's order history — all orders where `orders.customer_id` = this customer (guest orders are never visible) |
| GET | `/customers/me/addresses` | Customer | List saved addresses (default first) |
| POST | `/customers/me/addresses` | Customer | Add an address — `{label?, address_line1, address_line2?, city, state?, postal_code?, country, is_default?}` |
| PATCH | `/customers/me/addresses/:id` | Customer | Update any of the address fields (partial merge). `is_default: true` promotes this address and demotes the others |
| DELETE | `/customers/me/addresses/:id` | Customer | Remove an address → 204 (404 if it's not this customer's) |

**Scope isolation:** a customer JWT never passes an Admin route (`GET /orders` etc. → 403), and a merchant JWT never passes a customer route (`/customers/me` → 403). Signup/login are tenant-scoped like every public endpoint — the same email is a valid, distinct customer on each tenant.

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

## Notifications

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/notifications/log` | Admin | Delivery log for customer communications (order confirmations, delivery updates, welcome emails). Returns `sent` or `failed` status per attempt. |

**Provider:** transactional email uses Resend (configured via `RESEND_API_KEY` + `NOTIFICATIONS_FROM_EMAIL`); when unset a log-only provider is used so the surface is testable without external credentials. A failed send is recorded as `status=failed` in the log but **never fails the checkout or order update that triggered it**.

**Events wired:**
- `order.created` → order confirmation (to customer email/phone)
- `order.paid` (emitted when order reaches `delivered`) → delivery notification
- `customers.signup` → welcome email

## Store Settings

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/tenant/settings` | Admin | Fetch the current tenant's settings: `name`, `logo_media_asset_id`, `default_currency`, `timezone`, `support_email`, `support_phone`, `tax_rate_percent` |
| PATCH | `/tenant/settings` | Admin | Update any of the above settings (partial merge). `tax_rate_percent` must be 0–100 |

**Tax impact:** when `tax_rate_percent > 0`, checkout computes `tax_cents = subtotal_cents × tax_rate_percent / 100` and adds it to `total_cents` (alongside shipping, minus discount). The `tax_cents` snapshot is stored on the order.

## Platform

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/healthz` | Public | Liveness check |
| GET | `/metrics` | Internal | Prometheus scrape endpoint. Mounted always; when `METRICS_TOKEN` is set requires `Authorization: Bearer <token>` (block external exposure at the proxy/firewall too — never reverse-proxy public requests to it) |

## Error shape (every endpoint)

Every non-2xx response follows one shape. `code` is a stable machine string; when a status is produced generically the code defaults to `invalid_request` (400), `unauthorized` (401), `forbidden` (403), `not_found` (404), `conflict` (409), `upstream_error` (502), `internal_error` (5xx). A panic recovered by middleware renders as `internal_error` (no stack trace leaks).

```json
{ "error": { "code": "product_not_found", "message": "No product with that id in this store." } }
```

## Not built yet (deferred — see `docs/04-agent-build-spec.md`'s forward-compatibility section)

- `/webhooks/*` — subscriber endpoints for a future developer marketplace
- A public, versioned developer API (likely GraphQL) separate from this internal REST contract
- Any online payment method beyond Cash on Delivery
- Post/template revision history (undo to a previous saved version)
- Blog archive, search-results, and announcement-bar template types (schema supports them; not built in the v1 phase plan)
