DROP TABLE IF EXISTS merchant_account_stores;
DROP TABLE IF EXISTS merchant_accounts;

ALTER TABLE tenants DROP COLUMN IF EXISTS onboarding_completed;

ALTER TABLE merchant_users DROP CONSTRAINT IF EXISTS merchant_users_account_tenant_key;
ALTER TABLE merchant_users DROP COLUMN IF EXISTS account_id;