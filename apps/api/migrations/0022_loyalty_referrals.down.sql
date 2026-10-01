ALTER TABLE discounts DROP COLUMN customer_id;

ALTER TABLE tenants
  DROP COLUMN loyalty_points_per_currency_unit,
  DROP COLUMN loyalty_redemption_rate;

DROP INDEX loyalty_ledger_referral_once;
DROP INDEX loyalty_ledger_order_placed_once;
DROP INDEX loyalty_ledger_customer_idx;
DROP POLICY tenant_isolation ON loyalty_ledger;
DROP TABLE loyalty_ledger;

ALTER TABLE customers DROP COLUMN loyalty_points;