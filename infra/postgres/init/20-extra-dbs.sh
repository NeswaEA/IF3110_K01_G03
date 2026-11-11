#!/bin/sh
# Extra target databases (runs once on an empty data directory, after 01-roles.sh):
#  - demo_big: the large dataset for batch migrations (filled by `seed bigdata`)
#  - load_1..load_4: small databases for the k6 stress test (filled by `seed load`)
# Each gets the same reserved schema and privileges as demo.
set -eu

for db in demo_big load_1 load_2 load_3 load_4; do
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" -c "CREATE DATABASE $db"
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$db" <<SQL
CREATE SCHEMA _platform AUTHORIZATION platform_runner;
REVOKE ALL ON SCHEMA _platform FROM PUBLIC;
REVOKE ALL ON SCHEMA _platform FROM platform_editor;

GRANT CONNECT ON DATABASE $db TO platform_runner, platform_editor;
GRANT USAGE, CREATE ON SCHEMA public TO platform_editor, platform_runner;

ALTER DEFAULT PRIVILEGES FOR ROLE platform_editor IN SCHEMA public
  GRANT SELECT ON TABLES TO platform_runner;
ALTER DEFAULT PRIVILEGES FOR ROLE platform_editor IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO platform_runner;
SQL
done
