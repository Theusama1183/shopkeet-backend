REVOKE ALL ON ALL TABLES IN SCHEMA public FROM shopkeet_app;
ALTER TABLE merchant_users OWNER TO shopkeet;
ALTER TABLE tenants OWNER TO shopkeet;
ALTER SCHEMA public OWNER TO shopkeet;
ALTER ROLE shopkeet_app NOLOGIN;
DROP ROLE IF EXISTS shopkeet_app;