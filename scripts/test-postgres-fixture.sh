#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
target=${1:?provide an isolated test PostgreSQL container with /fixtures mounted}
fixture_dir=${2:-/fixtures}
psql_target() { docker exec -i "$target" psql -X -U target_admin -v ON_ERROR_STOP=1 "$@"; }
exists=$(psql_target -d postgres -Atc "SELECT count(*) FROM pg_database WHERE datname='demo_desired'")
[ "$exists" = 0 ] || { echo "test requires a fresh target without demo_desired" >&2; exit 1; }
psql_target -d postgres -v fixture_dir="$fixture_dir" < infra/postgres/seed-desired.sql
psql_target -d demo_desired <<'SQL'
DO $$ BEGIN
 IF (SELECT count(*) FROM public.items) <> 3 OR
    NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='items' AND column_name='status') OR
    to_regclass('public.item_tags') IS NULL THEN
  RAISE EXCEPTION 'desired fixture is incomplete';
 END IF;
END $$;
INSERT INTO public.item_tags VALUES(1,2,'keep');
CREATE TABLE _platform.migration_ledger(version integer PRIMARY KEY);
INSERT INTO _platform.migration_ledger VALUES(7);
SQL
psql_target -d postgres -v fixture_dir="$fixture_dir" < infra/postgres/seed-desired.sql
psql_target -d demo_desired <<'SQL'
DO $$ BEGIN
 IF (SELECT count(*) FROM public.items) <> 3 OR
    (SELECT label FROM public.item_tags WHERE id=1) IS DISTINCT FROM 'keep' OR
    (SELECT version FROM _platform.migration_ledger) IS DISTINCT FROM 7 THEN
  RAISE EXCEPTION 'fixture rerun changed existing data or ledger';
 END IF;
END $$;
SQL
echo "PostgreSQL fixture initialization and preservation tests passed."
