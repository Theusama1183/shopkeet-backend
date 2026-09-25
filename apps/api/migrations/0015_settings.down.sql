ALTER TABLE orders DROP COLUMN IF EXISTS tax_cents;
ALTER TABLE orders DROP COLUMN IF EXISTS internal_note;

ALTER TABLE tenants DROP COLUMN IF EXISTS tax_rate_percent;
ALTER TABLE tenants DROP COLUMN IF EXISTS support_phone;
ALTER TABLE tenants DROP COLUMN IF EXISTS support_email;
ALTER TABLE tenants DROP COLUMN IF EXISTS timezone;
ALTER TABLE tenants DROP COLUMN IF EXISTS default_currency;
ALTER TABLE tenants DROP COLUMN IF EXISTS logo_media_asset_id;