-- Phase 17: Abandoned Cart Recovery (down)

DROP INDEX IF EXISTS carts_recovery_scan_idx;

ALTER TABLE carts
  DROP COLUMN IF EXISTS customer_email,
  DROP COLUMN IF EXISTS last_activity_at,
  DROP COLUMN IF EXISTS recovery_sent_at;