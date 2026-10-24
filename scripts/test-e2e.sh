#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
docker info >/dev/null
docker compose version >/dev/null
stamp=$(date -u +%Y%m%d-%H%M%S)-$$
project=if3110-e2e-$stamp
src=$(pwd -W 2>/dev/null || pwd)
E2E_ARTIFACT_DIR=$src/output/playwright/$stamp
export E2E_ARTIFACT_DIR
mkdir -p "$E2E_ARTIFACT_DIR"
: > "$E2E_ARTIFACT_DIR/compose.env"
compose() { MSYS_NO_PATHCONV=1 docker compose --project-name "$project" --env-file "$E2E_ARTIFACT_DIR/compose.env" -f tests/e2e/compose.yml "$@"; }
cleanup() {
  code=$?
  trap - EXIT HUP INT TERM
  compose logs --no-color app > "$E2E_ARTIFACT_DIR/server.log" 2>&1 || true
  compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  echo "Browser artifacts: $E2E_ARTIFACT_DIR"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
compose build app browser
compose up -d --wait metadata target
compose run --rm sqlite
compose exec -T target psql -U target_admin -d postgres -v ON_ERROR_STOP=1 < infra/postgres/seed-desired.sql
compose run --rm --entrypoint /app/migrate app up
compose run --rm --entrypoint /app/migrate app up
compose run --rm --entrypoint /app/seed app fixtures
compose up -d app
if compose run --rm browser npm test -- "$@"; then result=0; else result=$?; fi
compose run --rm browser node check-results.mjs /artifacts/report.json
exit "$result"
