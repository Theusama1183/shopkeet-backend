-- Revert the RLS custom-GUC hardening: restore the original tenant_isolation
-- USING expression (bare current_setting(...)::uuid). Inverse of 0029.

DROP POLICY tenant_isolation ON merchant_users;
CREATE POLICY tenant_isolation ON merchant_users
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON media_assets;
CREATE POLICY tenant_isolation ON media_assets
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON categories;
CREATE POLICY tenant_isolation ON categories
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON products;
CREATE POLICY tenant_isolation ON products
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON product_categories;
CREATE POLICY tenant_isolation ON product_categories
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON product_images;
CREATE POLICY tenant_isolation ON product_images
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON carts;
CREATE POLICY tenant_isolation ON carts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON cart_items;
CREATE POLICY tenant_isolation ON cart_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON orders;
CREATE POLICY tenant_isolation ON orders
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON order_items;
CREATE POLICY tenant_isolation ON order_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON posts;
CREATE POLICY tenant_isolation ON posts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON templates;
CREATE POLICY tenant_isolation ON templates
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON sections;
CREATE POLICY tenant_isolation ON sections
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON redirects;
CREATE POLICY tenant_isolation ON redirects
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON product_options;
CREATE POLICY tenant_isolation ON product_options
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON product_option_values;
CREATE POLICY tenant_isolation ON product_option_values
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON product_variants;
CREATE POLICY tenant_isolation ON product_variants
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON product_variant_option_values;
CREATE POLICY tenant_isolation ON product_variant_option_values
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON shipping_zones;
CREATE POLICY tenant_isolation ON shipping_zones
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON shipping_rates;
CREATE POLICY tenant_isolation ON shipping_rates
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON discounts;
CREATE POLICY tenant_isolation ON discounts
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON customers;
CREATE POLICY tenant_isolation ON customers
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON customer_addresses;
CREATE POLICY tenant_isolation ON customer_addresses
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON notification_log;
CREATE POLICY tenant_isolation ON notification_log
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON idempotency_keys;
CREATE POLICY tenant_isolation ON idempotency_keys
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON returns;
CREATE POLICY tenant_isolation ON returns
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON return_items;
CREATE POLICY tenant_isolation ON return_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON product_reviews;
CREATE POLICY tenant_isolation ON product_reviews
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON gift_cards;
CREATE POLICY tenant_isolation ON gift_cards
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON back_in_stock_subscriptions;
CREATE POLICY tenant_isolation ON back_in_stock_subscriptions
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON loyalty_ledger;
CREATE POLICY tenant_isolation ON loyalty_ledger
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON wishlist_items;
CREATE POLICY tenant_isolation ON wishlist_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON bundles;
CREATE POLICY tenant_isolation ON bundles
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON bundle_items;
CREATE POLICY tenant_isolation ON bundle_items
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON quantity_breaks;
CREATE POLICY tenant_isolation ON quantity_breaks
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);

DROP POLICY tenant_isolation ON product_recommendations;
CREATE POLICY tenant_isolation ON product_recommendations
  USING (tenant_id = current_setting('app.current_tenant', true)::uuid);