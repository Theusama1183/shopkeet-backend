# Shopkeet — Hardening & Feature Expansion (Phases 14–24)

Addendum to `04-agent-build-spec.md` / `07-expansion-build-spec.md`, applied on top of the live Phase 0–13 backend. Same rules: work in order, don't start phase N+1 until phase N's acceptance criteria pass, `.agents/rules/*` still governs.

Two kinds of phases here, in this order for a reason — **fix reliability before adding surface area**:
- **Phase 14** is pure hardening (no merchant-visible feature).
- **Phases 15–24** are features, each framed against the paid Shopify app category it replaces, so a merchant on Shopkeet doesn't need to pay a third-party app subscription for it. Verified against current (2026) Shopify App Store data — pricing cited is what merchants actually pay elsewhere for this today.

---

## Phase 14 — Reliability Hardening (idempotency + rate limiting)

No new merchant-visible behavior — this closes two real production gaps.

**Idempotency keys** — a network retry or a double-tapped "Place Order" currently creates two orders.

```sql
CREATE TABLE idempotency_keys (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  key TEXT NOT NULL,
  endpoint TEXT NOT NULL,           -- e.g. 'POST /checkout'
  response_status INTEGER,
  response_body JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, endpoint, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON idempotency_keys
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

Middleware wraps `POST /checkout`, `POST /customers/signup`, `POST /cart/discount`, `POST /discounts`: read the `Idempotency-Key` header; if a row already exists for `(tenant_id, endpoint, key)`, return the cached response without re-running the handler; otherwise run it and cache the response before replying. Purge rows older than 24h via a scheduled Asynq job — don't rely on idempotency keys for longer-term dedup, only retry protection.

**Rate limiting** — nothing currently throttles anything. Use Redis (already in the stack) for per-route limits:

| Route | Limit | Key |
|---|---|---|
| `POST /auth/login` | 5 / 15 min | IP + email |
| `POST /auth/otp/send` | 5 / 15 min | IP + email |
| `POST /auth/otp/verify` | 15 / 15 min | IP + email |
| `POST /auth/forgot-password` | 5 / hour | IP + email |
| `POST /auth/reset-password` | 10 / 15 min | IP |
| `POST /customers/login` | 5 / 15 min | IP + email |
| `POST /auth/signup`, `POST /customers/signup` | 10 / hour | IP |
| `POST /cart/discount` | 20 / hour | cart/session |
| `POST /checkout` | 30 / hour | IP |

`POST /auth/otp/verify` additionally carries a **per-email guess budget in the database**, not Redis: failed attempts accumulate across code reissues (a re-login mints a fresh code but the old rows still count) and 20 within an hour answer `429 too_many_attempts`. The IP half of the key can be rotated away by a distributed attacker; this sum cannot.

429 responses include `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`, `Retry-After`. A single global limit that punishes all traffic equally is the wrong shape — these are per-route because a login brute-force and a checkout burst are different problems.

**Acceptance:** retrying `POST /checkout` with the same `Idempotency-Key` produces exactly one order; the 6th login attempt in 15 minutes for one email returns 429 with `Retry-After`, not 401.

---

## Phase 15 — Merchant Operations: Draft Orders & Returns

**Draft/manual orders** — for phone/WhatsApp orders merchants take outside the storefront, which is common in a COD-heavy market and has no Shopify-app equivalent worth paying for (it's core order-entry, not an add-on).

```sql
ALTER TABLE orders ADD COLUMN source TEXT NOT NULL DEFAULT 'storefront'; -- 'storefront', 'draft'
```

`POST /orders/draft` (Admin) — same shape as checkout, but merchant-initiated: no customer session required, an existing customer can be attached or a new one entered inline, line prices can be overridden. Still runs the Phase 4 `FOR UPDATE` stock check — a draft order is still a real order and must not oversell.

**Returns & exchanges** — replaces the return-tracking half of apps like Loop/AfterShip Returns (from ~$9–25/mo).

```sql
CREATE TABLE returns (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  order_id UUID NOT NULL REFERENCES orders(id),
  reason TEXT,
  status TEXT NOT NULL DEFAULT 'requested', -- requested, approved, received, refunded, rejected
  restock BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE returns ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON returns
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE return_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  return_id UUID NOT NULL REFERENCES returns(id),
  order_item_id UUID NOT NULL REFERENCES order_items(id),
  quantity INTEGER NOT NULL
);
ALTER TABLE return_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON return_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

`POST /orders/:id/returns` (Customer, via order lookup, or Admin), `PATCH /returns/:id/status` (Admin), `GET /returns` (Admin). Status → `received` with `restock=true` increments the relevant variant's `inventory_count`.

**Acceptance:** a draft order created by a merchant decrements stock the same way a storefront order does; a return marked `received` restocks the correct variant, not the product as a whole.

---

## Phase 16 — Product Reviews

Replaces **Judge.me** (free tier exists, but paid tiers from ~$15/mo) — reviews are the single most universally-installed paid app category on Shopify, and there's no reason it needs to be a third-party dependency.

```sql
CREATE TABLE product_reviews (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  product_id UUID NOT NULL REFERENCES products(id),
  customer_id UUID REFERENCES customers(id),
  order_id UUID REFERENCES orders(id),   -- present = verified purchase
  rating INTEGER NOT NULL CHECK (rating BETWEEN 1 AND 5),
  title TEXT,
  body TEXT,
  photo_media_asset_ids UUID[] NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'pending', -- pending, published, rejected
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE product_reviews ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON product_reviews
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE products
  ADD COLUMN rating_average NUMERIC(2,1) NOT NULL DEFAULT 0,
  ADD COLUMN rating_count INTEGER NOT NULL DEFAULT 0; -- recomputed whenever a review is published
```

`POST /products/:id/reviews` (Customer — auto-links `order_id` if they have a delivered order containing this product, marking it verified), `GET /products/:id/reviews` (Public, published only), `PATCH /reviews/:id` (Admin — approve/reject), `DELETE /reviews/:id` (Admin).

**Acceptance:** a review from a customer with a delivered order for that product is flagged verified; `rating_average`/`rating_count` update the moment a review is approved, not before.

---

## Phase 17 — Abandoned Cart Recovery & Lifecycle Emails

Replaces the core of **Klaviyo's** cart-recovery flow (free up to 250 contacts, then scales with list size). This is the highest-ROI item in this whole file: roughly 70% of carts are abandoned industry-wide, and a single well-timed recovery email is where most of a basic sequence's 10–17% recovery rate comes from — and the notification infrastructure (Resend, event bus) already exists from Phase 12.

```sql
ALTER TABLE carts
  ADD COLUMN customer_email TEXT,
  ADD COLUMN last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN recovery_sent_at TIMESTAMPTZ;
```

A scheduled Asynq job runs hourly: for carts where `last_activity_at < now() - interval '1 hour'`, `customer_email IS NOT NULL`, `recovery_sent_at IS NULL`, and no order exists for that session, send a `cart_abandoned` notification (via the existing `NotificationProvider`) and set `recovery_sent_at`. Keep v1 to one email — a second/third follow-up step is easy to add later once the first is proven out, not a reason to hold this phase.

**Acceptance:** a cart left idle for over an hour with a captured email triggers exactly one `cart_abandoned` notification, logged in `notification_log`; a cart that converts to an order before the hour is up never gets one.

---

## Phase 18 — Gift Cards

Native on Shopify itself, but still a real merchant need and worth having day one rather than bolted on later.

```sql
CREATE TABLE gift_cards (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  code TEXT NOT NULL,
  initial_balance_cents INTEGER NOT NULL,
  balance_cents INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'active', -- active, disabled
  expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, code)
);
ALTER TABLE gift_cards ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON gift_cards
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE orders
  ADD COLUMN gift_card_code TEXT,
  ADD COLUMN gift_card_cents INTEGER NOT NULL DEFAULT 0; -- amount applied, snapshot
```

`POST /gift-cards` (Admin, issue), `GET /gift-cards` (Admin), `POST /cart/gift-card` (Customer, apply — validates balance). Same claim pattern as discounts: re-validate and decrement `balance_cents` via `FOR UPDATE` inside the checkout transaction, not at apply-time, so concurrent use of a near-empty card can't double-spend it.

**Total formula becomes:** `total_cents = subtotal - discount_cents + shipping_cost_cents + tax_cents - gift_card_cents` (floored at 0; unused balance stays on the card).

**Acceptance:** two concurrent checkouts both trying to spend the last $5 of a $5 gift card — exactly one succeeds.

---

## Phase 19 — Pre-orders & Back-in-Stock Alerts

Replaces dedicated "Back in Stock" apps (typically $10–25/mo).

```sql
ALTER TABLE product_variants
  ADD COLUMN allow_preorder BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN preorder_ships_at TIMESTAMPTZ;

ALTER TABLE order_items ADD COLUMN is_preorder BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE back_in_stock_subscriptions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  variant_id UUID NOT NULL REFERENCES product_variants(id),
  email TEXT NOT NULL,
  notified_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE back_in_stock_subscriptions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON back_in_stock_subscriptions
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

Checkout allows a variant with `inventory_count=0 AND allow_preorder=true` (line marked `is_preorder=true`, no stock decrement). `POST /products/:id/variants/:variantId/notify-me` (Public, email capture) for out-of-stock, non-preorderable variants. When a variant's `PATCH` moves `inventory_count` from 0 to positive, a job emails every subscriber with `notified_at IS NULL` and sets it.

**Acceptance:** a preorder checkout succeeds against zero stock and doesn't touch `inventory_count`; restocking a variant notifies each waiting subscriber exactly once.

---

## Phase 20 — Loyalty & Referrals

Replaces **Smile.io**/**Gameball**-tier apps (free tiers exist; meaningful paid tiers run $49–199+/mo).

```sql
ALTER TABLE customers ADD COLUMN loyalty_points INTEGER NOT NULL DEFAULT 0;

CREATE TABLE loyalty_ledger (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  customer_id UUID NOT NULL REFERENCES customers(id),
  points INTEGER NOT NULL,          -- positive = earned, negative = redeemed
  reason TEXT NOT NULL,             -- 'order_placed', 'referral', 'redeemed', 'signup_bonus'
  order_id UUID REFERENCES orders(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE loyalty_ledger ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON loyalty_ledger
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

ALTER TABLE tenants
  ADD COLUMN loyalty_points_per_currency_unit INTEGER NOT NULL DEFAULT 0, -- 0 = loyalty disabled for this tenant
  ADD COLUMN loyalty_redemption_rate INTEGER NOT NULL DEFAULT 100;        -- points needed per 1 currency unit of discount
```

An order reaching `delivered` (subscribing to the existing `order.paid` event from Phase 12) earns points for its `customer_id`, if set. Referrals reuse the `discounts` table — a unique per-customer code, `type='fixed_amount'`, tracked back to the referring customer for the points payout.

`GET /customers/me/loyalty` (Customer — balance + ledger), `POST /loyalty/redeem` (Customer — converts points into a one-time discount code applied to the current cart).

**Acceptance:** a delivered order credits points once (not on every status change); redeeming more points than the balance allows is rejected.

---

## Phase 21 — Advanced & Automatic Discounts

Replaces **Bold Discounts**-type apps (BOGO, tiered, automatic no-code promotions).

```sql
ALTER TABLE discounts
  ADD COLUMN applies_to TEXT NOT NULL DEFAULT 'order', -- 'order', 'shipping', 'product'
  ADD COLUMN buy_quantity INTEGER,   -- BOGO: buy X
  ADD COLUMN get_quantity INTEGER,   -- get Y at a reduced/zero price
  ADD COLUMN requires_code BOOLEAN NOT NULL DEFAULT true; -- false = applies automatically, no code entry
```

At checkout, after any entered code is applied, check all `status='active', requires_code=false` discounts for the tenant and apply the single best-value one automatically — **v1 does not stack multiple automatic discounts**, to avoid combinatorial edge cases; revisit only if a real merchant need shows up. `applies_to='shipping'` is how "free shipping over $X" is expressed, reusing this table instead of a separate mechanism.

**Acceptance:** an automatic free-shipping discount applies with no code entered once the cart clears its `min_subtotal_cents`; a code-based and an automatic discount active at once — only the automatic one applies (no stacking), and this is deterministic, not order-dependent.

---

## Phase 22 — Wishlist

Replaces **Wishlist Plus**-type apps (typically $10–15/mo).

```sql
CREATE TABLE wishlist_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL REFERENCES tenants(id),
  customer_id UUID NOT NULL REFERENCES customers(id),
  product_id UUID NOT NULL REFERENCES products(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (customer_id, product_id)
);
ALTER TABLE wishlist_items ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON wishlist_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
```

`GET/POST/DELETE /customers/me/wishlist[/:productId]` (Customer). Small phase, low risk — a good one to build quickly.

---

## Phase 23 — Order Tracking Fields & PDF Invoices

Replaces the tracking-page half of **AfterShip** (free tier exists, paid from ~$10/mo) and invoice-generator apps like **Sufio**.

```sql
ALTER TABLE orders
  ADD COLUMN tracking_number TEXT,
  ADD COLUMN tracking_carrier TEXT,
  ADD COLUMN tracking_url TEXT;
```

Merchant sets these when transitioning an order to `shipped` (existing `PATCH /orders/:id/status`, extend the body to accept them optionally on that transition). The existing customer-facing order lookup (`GET /orders/:id?phone=`) already returns the full order — just include these fields, no new endpoint.

`GET /orders/:id/invoice.pdf` (Admin, and Customer via the same phone/email lookup) — generates a simple line-itemized PDF server-side. No new table.

**Acceptance:** an order moved to `shipped` with tracking info returns it in the customer-facing lookup; the invoice PDF total matches the order's `total_cents` exactly, including tax/shipping/discount/gift-card lines.

---

## Phase 24 — Storefront Analytics Dashboard

Replaces the basic tier of apps like **Lifetimely**/**Triple Whale** (often $40–100+/mo). No new tables — this is aggregation over data that already exists.

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/analytics/sales?period=7d\|30d\|90d` | Admin | Revenue and order count over time |
| GET | `/analytics/top-products?period=` | Admin | Best-selling products/variants by quantity and revenue |
| GET | `/analytics/conversion?period=` | Admin | Carts created vs. orders placed — a basic funnel |

Straightforward `GROUP BY date_trunc(...)` queries; add indexes on `orders.created_at` and confirm `order_items.order_id` is indexed (it is, via the FK) before shipping this — these queries get slow fast without them once a store has real order volume.

**Acceptance:** sales-over-time totals reconcile exactly against a manual `SUM(total_cents)` over the same period; top-products ranks by the metric requested (quantity vs. revenue), not always the same one.

---

## Deliberately still deferred (and why)

| Feature | Why it's not here |
|---|---|
| Subscriptions / recurring billing | Needs a real online payment gateway to auto-charge — not possible on Cash on Delivery. Revisit only alongside adding online payments. |
| Multi-location inventory | Real complexity (routing orders to the right warehouse); no current merchant has more than one location. |
| Live chat | Better integrated as a third-party widget (e.g. a free-tier chat script) than built from scratch — a different problem (real-time messaging infra) than everything else in this file. |
| Multi-currency / regional pricing (Shopify Markets equivalent) | Ties to future international expansion; premature for the current target market. |
| B2B/wholesale tiers, multi-entity | Enterprise-tier even for Shopify itself (Plus-only); not relevant to solo/small merchants. |
| SEO audit tooling | The underlying fields (`meta_title`, `meta_description`) already exist from Phase 6 — an automated "audit and suggest" UI is a frontend nicety for later, not a backend gap. |
