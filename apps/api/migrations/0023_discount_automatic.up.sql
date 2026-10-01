-- Phase 21: Advanced & Automatic Discounts (Bold Discounts replacement).
--
-- discounts gains an applies scope and a requires_code flag:
--   - requires_code = false  → the discount applies *automatically* at checkout,
--     no code entry. v1 applies the single best-value automatic discount and
--     never stacks it with an entered code or another automatic one (see the
--     AutoPick/Claim design in internal/discounts and the checkout flow in
--     internal/orders).
--   - applies_to = 'shipping' → expresses "free shipping over $X" by reusing
--     min_subtotal_cents as the goods-subtotal threshold; the value (percentage
--     or fixed_amount) is applied to the shipping cost instead of the subtotal.
--   - applies_to = 'product' with buy_quantity/get_quantity → reserved for BOGO;
--     schema only this phase, no checkout logic yet (rejected with a clear 400).
--
-- automatic discounts have no code, so code becomes nullable (Postgres UNIQUE
-- treats NULLs as distinct — the existing UNIQUE (tenant_id, code) still holds).

ALTER TABLE discounts
  ADD COLUMN applies_to TEXT NOT NULL DEFAULT 'order',
  ADD COLUMN buy_quantity INTEGER,
  ADD COLUMN get_quantity INTEGER,
  ADD COLUMN requires_code BOOLEAN NOT NULL DEFAULT true;

ALTER TABLE discounts
  ALTER COLUMN code DROP NOT NULL,
  ADD CONSTRAINT discounts_applies_to_check
    CHECK (applies_to IN ('order', 'shipping', 'product'));