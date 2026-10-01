-- Phase 23: Order Tracking Fields. Reversible.
ALTER TABLE orders
  DROP COLUMN tracking_number,
  DROP COLUMN tracking_carrier,
  DROP COLUMN tracking_url;