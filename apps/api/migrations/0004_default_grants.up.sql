-- Future tables created by the superuser migration role (shopkeet) must be
-- usable by the app role automatically — otherwise every new phase would need
-- an extra grant statement and a miss would silently break that phase's RLS.
-- These defaults apply to objects created by `shopkeet` from now on.
ALTER DEFAULT PRIVILEGES FOR ROLE shopkeet IN SCHEMA public
  GRANT ALL ON TABLES TO shopkeet_app;
ALTER DEFAULT PRIVILEGES FOR ROLE shopkeet IN SCHEMA public
  GRANT ALL ON SEQUENCES TO shopkeet_app;