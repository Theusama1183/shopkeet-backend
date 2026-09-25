DROP TABLE IF EXISTS shipping_rates;
DROP TABLE IF EXISTS shipping_zones;

-- shipping_address went nullable in the up migration; restore its historical
-- NOT NULL by backfilling from the structured address columns before dropping
-- them.
UPDATE orders SET shipping_address = o.trimmed_address
FROM (
  SELECT id,
         trim(BOTH ', ' FROM
           nullif(concat_ws(', ', shipping_address_line1, shipping_address_line2,
                            shipping_city, shipping_state, shipping_postal_code,
                            shipping_country), '')) AS trimmed_address
  FROM orders
) o
WHERE orders.id = o.id AND orders.shipping_address IS NULL AND o.trimmed_address IS NOT NULL;
ALTER TABLE orders ALTER COLUMN shipping_address SET NOT NULL;

ALTER TABLE orders
  DROP COLUMN IF EXISTS shipping_cost_cents,
  DROP COLUMN IF EXISTS shipping_method,
  DROP COLUMN IF EXISTS shipping_country,
  DROP COLUMN IF EXISTS shipping_postal_code,
  DROP COLUMN IF EXISTS shipping_state,
  DROP COLUMN IF EXISTS shipping_city,
  DROP COLUMN IF EXISTS shipping_address_line2,
  DROP COLUMN IF EXISTS shipping_address_line1;