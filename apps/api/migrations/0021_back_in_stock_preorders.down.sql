DROP POLICY tenant_isolation ON back_in_stock_subscriptions;
DROP TABLE back_in_stock_subscriptions;

ALTER TABLE order_items DROP COLUMN is_preorder;

ALTER TABLE product_variants
  DROP COLUMN allow_preorder,
  DROP COLUMN preorder_ships_at;