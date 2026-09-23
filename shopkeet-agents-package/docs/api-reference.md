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
| PATCH | `/cart/items/:id` | Customer | Change quantity |
| DELETE | `/cart/items/:id` | Customer | Remove an item |

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
