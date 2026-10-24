\set ON_ERROR_STOP on
\if :{?fixture_dir}
\else
\set fixture_dir /fixtures
\endif
\set source_fixture :fixture_dir '/demo.sql'
\set desired_fixture :fixture_dir '/desired.sql'
SELECT pg_advisory_lock(1397048645);
SELECT 'CREATE DATABASE demo_desired OWNER platform_runner'
 WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname='demo_desired')
\gexec
\connect demo_desired
BEGIN;
SELECT pg_advisory_xact_lock(1397048645);
SELECT NOT EXISTS (
    SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
     WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg\_%'
) AND NOT EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
     WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg\_%'
) AND NOT EXISTS (
    SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
     WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg\_%'
) AND NOT EXISTS (
    SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('public','_platform','pg_catalog','information_schema') AND nspname NOT LIKE 'pg\_%'
) AS initialize_desired
\gset
\if :initialize_desired
CREATE SCHEMA IF NOT EXISTS _platform AUTHORIZATION platform_runner;
REVOKE ALL ON SCHEMA _platform FROM PUBLIC,platform_editor;
GRANT USAGE,CREATE ON SCHEMA public TO platform_editor;
ALTER DEFAULT PRIVILEGES FOR ROLE platform_editor IN SCHEMA public GRANT SELECT ON TABLES TO platform_runner;
ALTER DEFAULT PRIVILEGES FOR ROLE platform_editor IN SCHEMA public GRANT USAGE,SELECT ON SEQUENCES TO platform_runner;
\i :source_fixture
\i :desired_fixture
\echo 'Initialized PostgreSQL demo_desired.'
\else
\echo 'PostgreSQL demo_desired already has objects; kept as is.'
\endif
COMMIT;
