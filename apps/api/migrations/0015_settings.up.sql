-- Phase 13: Store Settings, Order Notes & Tax.
-- Adds tenant-level settings (logo, currency, timezone, support contact, tax rate)
-- and order-level fields (internal_note for merchants, tax_cents snapshot).
-- Pure DDL (ADD COLUMN only), runs as shopkeet_app.

-- ============================================================ tenants

ALTER TABLE tenants
  ADD COLUMN logo_media_asset_id UUID REFERENCES media_assets(id),
  ADD COLUMN default_currency TEXT NOT NULL DEFAULT 'usd',
  ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC',
  ADD COLUMN support_email TEXT,
  ADD COLUMN support_phone TEXT,
  ADD COLUMN tax_rate_percent INTEGER NOT NULL DEFAULT 0;

-- ============================================================ orders

ALTER TABLE orders
  ADD COLUMN internal_note TEXT,
  ADD COLUMN tax_cents INTEGER NOT NULL DEFAULT 0;