-- RLS enforcement requires the app's DB user to be a *non-superuser* owner of
-- the tenant tables: Postgres superusers bypass row security entirely (even
-- with FORCE). The postgres image's POSTGRES_USER (shopkeet) is a superuser,
-- so Phase 1 creates a dedicated app role here; the API and migrate tool
-- connect as this role from now on.

-- Idempotent role bootstrap (safe to re-run).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'shopkeet_app') THEN
    CREATE ROLE shopkeet_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
  END IF;
END
$$;
ALTER ROLE shopkeet_app WITH PASSWORD 'shopkeet_app';

-- Make the app role the owner of the application schema/tables so tables it
-- creates (via future migrations) are owned by it, and RLS + FORCE bind.
ALTER SCHEMA public OWNER TO shopkeet_app;
GRANT CONNECT ON DATABASE shopkeet TO shopkeet_app;
ALTER TABLE tenants OWNER TO shopkeet_app;
ALTER TABLE merchant_users OWNER TO shopkeet_app;

-- Future tables: whatever the app role creates during a migration is owned by
-- it automatically (creator = owner); grants on the schema keep it usable.
GRANT ALL ON ALL TABLES IN SCHEMA public TO shopkeet_app;