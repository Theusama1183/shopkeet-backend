-- Phase 23: Order Tracking Fields & PDF Invoices (AfterShip/Sufio replacement).
-- Three nullable tracking columns on orders; the merchant sets them when
-- advancing an order to shipped (PATCH /orders/:id/status accepts them
-- optionally). The invoice PDF is generated server-side from existing
-- orders/order_items rows — no new table.

ALTER TABLE orders
  ADD COLUMN tracking_number TEXT,
  ADD COLUMN tracking_carrier TEXT,
  ADD COLUMN tracking_url TEXT;