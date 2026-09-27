-- Phase 17: Abandoned Cart Recovery & Lifecycle Emails (Klaviyo replacement)
-- The delivery Job (out of scope) covers the second-half triggers; the one
-- high-ROI piece here is the recovery email: a scheduled job (Phase 14 worker)
-- mails carts that sat idle > 1h with a captured email and no order, once.

ALTER TABLE carts
  ADD COLUMN customer_email  TEXT,                                    -- captured for recovery
  ADD COLUMN last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),     -- touch on every cart mutation
  ADD COLUMN recovery_sent_at TIMESTAMPTZ;                            -- set once by the sweep job

-- Partial index scoped to candidates the hourly sweep can consult cheaply.
CREATE INDEX carts_recovery_scan_idx ON carts (tenant_id, last_activity_at)
  WHERE customer_email IS NOT NULL AND recovery_sent_at IS NULL;