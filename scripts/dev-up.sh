#!/bin/sh
# Start the metadata and target PostgreSQL 17.11 containers (and load the demo fixture on first start).
# Usage: scripts/dev-up.sh            (needs .env, copied from .env.example)
#        scripts/dev-up.sh down -v    (stop and delete the data volumes)
set -eu
cd "$(dirname "$0")/.."
[ -f .env ] || { echo "missing .env: cp .env.example .env and fill the passwords" >&2; exit 1; }
if [ "$#" -gt 0 ]; then
  exec docker compose --env-file .env -f infra/docker/compose.yml "$@"
fi
exec docker compose --env-file .env -f infra/docker/compose.yml up -d --wait metadata-postgres target-postgres
