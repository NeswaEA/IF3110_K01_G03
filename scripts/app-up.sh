#!/bin/sh
# Build the app image, apply the metadata migrations and start the app at http://localhost:8080.
# Needs .env (scripts/init-env.sh) and the databases (scripts/dev-up.sh).
# Usage: scripts/app-up.sh        build, migrate, start
#        scripts/app-up.sh seed   also load the demo users, groups, grants and connections
set -eu
cd "$(dirname "$0")/.."
[ -f .env ] || { echo "missing .env: run scripts/init-env.sh" >&2; exit 1; }
src=$(pwd -W 2>/dev/null || pwd) # Git Bash on Windows needs the Windows path for -v
# MSYS_NO_PATHCONV stops Git Bash from rewriting /app/... into a Windows path
compose() { MSYS_NO_PATHCONV=1 docker compose --env-file .env -f infra/docker/compose.yml --profile app "$@"; }

compose build backend
compose run --rm --entrypoint /app/migrate backend up
if [ "${1:-}" = seed ]; then
  [ -f tmp/connections.local.json ] || { echo "missing tmp/connections.local.json: run scripts/init-env.sh" >&2; exit 1; }
  # the app reaches the target database by its Compose service name, not the host port
  sed -e 's/"host": "localhost"/"host": "target-postgres"/' -e 's/"port": 15433/"port": 5432/' \
    tmp/connections.local.json > tmp/connections.compose.json
  compose run --rm --entrypoint /app/seed -v "$src/tmp:/fixtures:ro" \
    -e FIXTURE_CONNECTIONS_FILE=/fixtures/connections.compose.json backend fixtures
fi
compose up -d --wait backend
echo "app is running at http://localhost:8080"
