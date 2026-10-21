#!/bin/sh
# Target PostgreSQL bootstrap (runs once on an empty data directory).
#  - platform_editor: restricted role the runner switches to (SET LOCAL ROLE) while user SQL runs
#  - platform_runner: login role used by the application; owns the reserved _platform schema
# User SQL under platform_editor cannot touch _platform (the migration ledger).
set -eu

: "${TARGET_RUNNER_PASSWORD:?TARGET_RUNNER_PASSWORD is required}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
     -v runner_pw="$TARGET_RUNNER_PASSWORD" <<'SQL'
CREATE ROLE platform_editor NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
CREATE ROLE platform_runner LOGIN PASSWORD :'runner_pw' NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT platform_editor TO platform_runner;

CREATE SCHEMA _platform AUTHORIZATION platform_runner;
REVOKE ALL ON SCHEMA _platform FROM PUBLIC;
REVOKE ALL ON SCHEMA _platform FROM platform_editor;

GRANT CONNECT ON DATABASE demo TO platform_runner, platform_editor;
GRANT USAGE, CREATE ON SCHEMA public TO platform_editor, platform_runner;

-- the runner can read what the editor creates, so viewer and diff work on user objects
ALTER DEFAULT PRIVILEGES FOR ROLE platform_editor IN SCHEMA public
  GRANT SELECT ON TABLES TO platform_runner;
ALTER DEFAULT PRIVILEGES FOR ROLE platform_editor IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO platform_runner;
SQL
