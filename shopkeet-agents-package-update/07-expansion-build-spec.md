# Shopkeet — Backend Expansion Spec (Phases 8–13)

This is an addendum to `04-agent-build-spec.md`, applied **on top of the already-deployed backend** (Phases 0–7 live). It is not a fresh-start spec — every schema change below is written as `ALTER`/new `CREATE TABLE` against the existing database, not a rewrite. Continue your migration numbering sequentially from wherever your last applied migration left off.

Same rules apply as before: work through phases **in order**, don't start the next until the current one's acceptance criteria pass with an automated test, and everything in `.agents/rules/backend-constraints.md` and `.agents/rules/conventions.md` still governs this code.

---

## Phase 8 — Product Variants

The biggest structural gap: products are currently flat (one price, one stock count). Almost no real store sells only single-variant items.

**Design decision: every product always has at least one variant.** A simple product gets one auto-created "Default" variant with no option values — this means `cart_items` and `order_items` reference `variant_id` uniformly, with no separate code path for "has variants" vs. "doesn't."

**New tables:**
```sql
CREATE TABLE product_options (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  name TEXT NOT NULL,           -- e.g. 'Size', 'Color'
  sort_order INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE product_options ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_options
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE product_option_values (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  option_id UUID NOT NULL REFERENCES product_options(id),
  value TEXT NOT NULL,          -- e.g. 'Small', 'Red'
  sort_order INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE product_option_values ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_option_values
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE product_variants (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  sku TEXT,
  price_cents INTEGER NOT NULL,
  inventory_count INTEGER NOT NULL DEFAULT 0,
  weight_grams INTEGER,
  status TEXT NOT NULL DEFAULT 'active', -- active, archived
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, sku)
);
ALTER TABLE product_variants ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_variants
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE product_variant_option_values (
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  variant_id UUID NOT NULL REFERENCES product_variants(id),
  option_value_id UUID NOT NULL REFERENCES product_option_values(id),
  PRIMARY KEY (variant_id, option_value_id)
);
ALTER TABLE product_variant_option_values ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_variant_option_values
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

**Alter existing tables:**
```sql
ALTER TABLE cart_items  ADD COLUMN variant_id UUID REFERENCES product_variants(id);
ALTER TABLE order_items ADD COLUMN variant_id UUID REFERENCES product_variants(id);
```

**Migration/backfill order:**
1. Create the new tables above.
2. Backfill one default variant per existing product: `INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status) SELECT tenant_id, id, price_cents, inventory_count, status FROM products;`
3. Backfill `order_items.variant_id` by matching `product_id` to that product's newly-created default variant (real order history — don't drop it).
4. `cart_items` are transient/session data — safe to truncate rather than backfill.
5. Once backfilled, set both new columns `NOT NULL`.
6. `products.price_cents` becomes a cached "starting at" display value — the API recomputes it as `MIN(active variant price_cents)` whenever a product's variants change. `products.inventory_count` becomes a cached sum of variant stock for quick display, same rule.

**Checkout impact:** the `FOR UPDATE` stock lock at checkout now targets `product_variants`, not `products`.

**Endpoints (extends `docs/api-reference.md` §Catalog):**

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/products/:id/options` | Admin | Create an option (e.g. "Size") with initial values |
| POST | `/products/:id/variants` | Admin | Create a variant: option-value combination, SKU, price, stock |
| PATCH | `/products/:id/variants/:variantId` | Admin | Update a variant |
| DELETE | `/products/:id/variants/:variantId` | Admin | Remove a variant |
| GET | `/products/:id` | Public/Admin | *(existing endpoint, response now embeds `options[]` and `variants[]`)* |

**Cart/Checkout:** `POST /cart` now takes `variant_id` (not `product_id`).

**Acceptance:** a product with two options (Size, Color) and multiple variants works end to end; cart requires a `variant_id`; checkout decrements the correct variant's stock; two concurrent checkouts for the last unit of *one specific variant* — exactly one succeeds, while other variants of the same product remain independently purchasable.

---

## Phase 9 — Shipping

**Alter `orders`** (add structured fields; keep the old `shipping_address` column in place, deprecated, rather than dropping it — it holds real historical order data):
```sql
ALTER TABLE orders
  ADD COLUMN shipping_address_line1 TEXT,
  ADD COLUMN shipping_address_line2 TEXT,
  ADD COLUMN shipping_city TEXT,
  ADD COLUMN shipping_state TEXT,
  ADD COLUMN shipping_postal_code TEXT,
  ADD COLUMN shipping_country TEXT,
  ADD COLUMN shipping_method TEXT,          -- snapshot of the rate name chosen at order time
  ADD COLUMN shipping_cost_cents INTEGER NOT NULL DEFAULT 0;
-- shipping_address (old TEXT column): stop writing to it going forward; leave existing rows as historical record.
```

**New tables:**
```sql
CREATE TABLE shipping_zones (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  name TEXT NOT NULL,                        -- e.g. "Punjab", "Rest of country"
  countries TEXT[] NOT NULL DEFAULT '{}',    -- ISO country codes covered
  regions TEXT[] NOT NULL DEFAULT '{}',      -- optional finer state/province match
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE shipping_zones ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON shipping_zones
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE shipping_rates (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  zone_id UUID NOT NULL REFERENCES shipping_zones(id),
  name TEXT NOT NULL,                        -- e.g. "Standard", "Express"
  rate_cents INTEGER NOT NULL,
  free_over_cents INTEGER,                   -- NULL = never free; else free when subtotal >= this
  sort_order INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE shipping_rates ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON shipping_rates
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

**Endpoints:**

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/shipping/rates?country=&state=` | Public | Matching rates for checkout to present as options. Response adds `state_required: true` when the country has region-restricted zones and no state was given, so the frontend can require the state field before checkout |
| POST/PATCH/DELETE | `/shipping/zones[/:id]` | Admin | Manage zones |
| POST/PATCH/DELETE | `/shipping/rates[/:id]` | Admin | Manage rates |

**Checkout impact:** `POST /checkout` now requires the structured address fields and a `shipping_rate_id`. The resolved `rate_cents` (after applying `free_over_cents` if it qualifies) is snapshotted into `orders.shipping_cost_cents` and `shipping_method` at order time — rates can change later without altering past orders.

**Acceptance:** a zone with two rates (e.g. Standard/Express) and a free-over-threshold; checkout in that zone shows both options, the correct cost is added to the total, and the threshold correctly waives the cost when it's met.

---

## Phase 10 — Discounts

```sql
CREATE TABLE discounts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  code TEXT NOT NULL,
  type TEXT NOT NULL,                  -- 'percentage', 'fixed_amount'
  value_percent INTEGER,               -- for 'percentage', 1–100
  value_cents INTEGER,                 -- for 'fixed_amount'
  min_subtotal_cents INTEGER,          -- NULL = no minimum
  starts_at TIMESTAMPTZ,
  ends_at TIMESTAMPTZ,
  usage_limit INTEGER,                 -- NULL = unlimited
  times_used INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active', -- active, disabled
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);
ALTER TABLE discounts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON discounts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

**Alter existing tables:**
```sql
ALTER TABLE carts  ADD COLUMN discount_code TEXT;
ALTER TABLE orders ADD COLUMN discount_code TEXT,
                    ADD COLUMN discount_cents INTEGER NOT NULL DEFAULT 0;
```

**Endpoints:**

| Method | Path | Auth | Description |
|---|---|---|---|
| POST/GET/PATCH/DELETE | `/discounts[/:id]` | Admin | Manage discount codes |
| POST | `/cart/discount` | Customer | Apply a code to the current cart (validated: active, in date range, subtotal minimum met) |

**Important:** re-validate the code again at checkout time (not just at apply-time) — it may have expired or hit its usage limit between the two. Increment `times_used` inside the same `FOR UPDATE` transaction that creates the order, to prevent overuse under concurrent checkouts.

**Acceptance:** a percentage-off code correctly reduces the order total; a code that's expired or past its usage limit is rejected at checkout even if it was applied to the cart earlier; concurrent checkouts against a code with `usage_limit=1` — exactly one succeeds.

---

## Phase 11 — Customer Accounts

```sql
CREATE TABLE customers (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  email TEXT,
  phone TEXT,
  password_hash TEXT,           -- NULL = record created from a guest order, no login set up yet
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);
ALTER TABLE customers ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customers
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE customer_addresses (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  customer_id UUID NOT NULL REFERENCES customers(id),
  label TEXT,                   -- e.g. "Home", "Office"
  address_line1 TEXT NOT NULL,
  address_line2 TEXT,
  city TEXT NOT NULL,
  state TEXT,
  postal_code TEXT,
  country TEXT NOT NULL,
  is_default BOOLEAN NOT NULL DEFAULT false
);
ALTER TABLE customer_addresses ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON customer_addresses
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

**Alter existing tables:**
```sql
ALTER TABLE orders ADD COLUMN customer_id UUID REFERENCES customers(id); -- nullable: guest checkout still allowed
```

**Auth:** customer JWTs are a distinct scope from merchant JWTs — payload `{tenant_id, customer_id, scope: "customer"}` vs. the existing merchant `{tenant_id, user_id, role, scope: "merchant"}`. Middleware checks `scope` per route group; a customer token must never pass the Admin auth check and vice versa.

**Endpoints:**

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/customers/signup` | Public | Create a customer account |
| POST | `/customers/login` | Public | Returns a customer-scoped JWT |
| GET | `/customers/me` | Customer | Current customer's profile |
| GET | `/customers/me/orders` | Customer | Their order history (via `customer_id`) |
| GET/POST/PATCH/DELETE | `/customers/me/addresses[/:id]` | Customer | Manage saved addresses |

**Acceptance:** a customer registers, logs in, and sees their past orders; guest checkout still works fully unauthenticated with `customer_id` left NULL; a customer JWT cannot access any `/products` Admin endpoint.

---

## Phase 12 — Order & Account Notifications

Nothing currently sends any customer communication — no order confirmation, nothing. This is a real gap, not a nice-to-have.

**Provider:** transactional email needs a real deliverability reputation that self-hosted SMTP can't practically provide at this stage — this is the same category of trade-off as R2. Recommended default: **Resend** (not open source — a flagged exception, same as Cloudflare and R2). Build it behind an interface so it's swappable:

```go
type NotificationProvider interface {
    Name() string // "resend", ...
    Send(ctx context.Context, n Notification) error
}
```

```sql
CREATE TABLE notification_log (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  notification_type TEXT NOT NULL, -- 'order_confirmation', 'order_shipped', 'order_delivered', 'customer_welcome'
  recipient TEXT NOT NULL,         -- email or phone
  order_id UUID REFERENCES orders(id),
  status TEXT NOT NULL DEFAULT 'sent', -- sent, failed
  sent_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE notification_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notification_log
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

**Wiring:** subscribe to the internal event bus already built in Phase 5/6 (`internal/platform/events`) — `order.created` → send confirmation, `order.paid`/order reaching `delivered` → send an update, `customers.signup` → welcome email. Don't hardcode the send call into the checkout handler directly; go through the same event seam already designed for the future webhooks module.

**Env vars:** `RESEND_API_KEY`, `NOTIFICATIONS_FROM_EMAIL`.

**Endpoint:**

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/notifications/log` | Admin | Delivery log, for debugging failed sends |

**Acceptance:** completing checkout produces an order confirmation (real send, or logged if the provider call fails — a failed send must not fail or crash the checkout request itself); marking an order `delivered` produces a second notification.

---

## Phase 13 — Store Settings, Order Notes & Tax

Small, quick additions — no new tables.

```sql
ALTER TABLE tenants
  ADD COLUMN logo_media_asset_id UUID REFERENCES media_assets(id),
  ADD COLUMN default_currency TEXT NOT NULL DEFAULT 'usd',
  ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC',
  ADD COLUMN support_email TEXT,
  ADD COLUMN support_phone TEXT,
  ADD COLUMN tax_rate_percent INTEGER NOT NULL DEFAULT 0; -- single flat rate; a real multi-jurisdiction tax
                                                            -- engine is a deliberately deferred, later feature

ALTER TABLE orders
  ADD COLUMN internal_note TEXT,       -- merchant-only, never shown to the customer
  ADD COLUMN tax_cents INTEGER NOT NULL DEFAULT 0;
```

**Endpoints:**

| Method | Path | Auth | Description |
|---|---|---|---|
| GET/PATCH | `/tenant/settings` | Admin | Store name, logo, currency, timezone, support contact, tax rate |
| PATCH | `/orders/:id/note` | Admin | Set/update the internal note |

**Checkout impact:** `tax_cents = (subtotal_cents - discount_cents) * tenants.tax_rate_percent / 100` — tax applies to what the customer actually pays for goods (post-discount), then shipping is added into `total_cents`.

**Acceptance:** a tenant with a 5% tax rate shows the correct `tax_cents` on a new order; an internal note set by the merchant never appears in any customer-facing (`Public`/`Customer`-auth) response.

---

## What's still deliberately deferred after this

Reviews, wishlists, abandoned-cart recovery, an analytics dashboard, granular staff permissions beyond `owner`/`staff`, refund tracking beyond order status, and a real multi-jurisdiction tax engine. All genuinely fine to leave for later — none of them block a store from functioning end to end the way the Phase 8–13 gaps did.
