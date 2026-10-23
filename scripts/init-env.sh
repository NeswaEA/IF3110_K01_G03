#!/bin/sh
# Create .env from .env.example and fill the local secrets with random values.
# Only empty keys are filled, so it is safe to rerun and never overwrites a value you set.
# Usage: scripts/init-env.sh
# Passwords apply only when the Compose volumes are created; if the databases already
# exist with other passwords, reset them with `scripts/dev-up.sh down -v` (deletes their data).
set -eu
cd "$(dirname "$0")/.."
root=$(pwd -W 2>/dev/null || pwd) # Git Bash on Windows needs the Windows path for the server

hex() { head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
get() { sed -n "s/^$1=//p" .env; }
# set_if_empty KEY VALUE replaces the line "KEY=" only; filled keys are left alone.
set_if_empty() {
  awk -v k="$1" -v v="$2" '$0 == k "=" { print k "=" v; next } { print }' .env > .env.tmp
  mv .env.tmp .env
}

[ -f .env ] || { cp .env.example .env; echo "created .env from .env.example"; }

set_if_empty METADATA_DB_PASSWORD "$(hex)"
set_if_empty TARGET_ADMIN_PASSWORD "$(hex)"
set_if_empty TARGET_RUNNER_PASSWORD "$(hex)"
set_if_empty FIXTURE_PASSWORD "$(hex)"
set_if_empty CONNECTION_ENCRYPTION_KEY "$(head -c 32 /dev/urandom | base64 | tr -d '\n')"

meta=$(get METADATA_DB_PASSWORD)
runner=$(get TARGET_RUNNER_PASSWORD)
set_if_empty METADATA_DATABASE_URL "postgres://metadata_owner:$meta@localhost:15432/metadata?sslmode=disable"
# the backend tests run inside a container (scripts/test-backend.sh), which reaches Compose through the host
set_if_empty TEST_METADATA_DATABASE_URL "postgres://metadata_owner:$meta@host.docker.internal:15432/metadata?sslmode=disable"
set_if_empty TEST_TARGET_DATABASE_URL "postgres://platform_runner:$runner@host.docker.internal:15433/demo?sslmode=disable"

mkdir -p tmp/sqlite
set_if_empty SQLITE_TARGET_DIR "$root/tmp/sqlite"
if [ ! -f tmp/connections.local.json ]; then
  sed "s/\"password\": \"\"/\"password\": \"$runner\"/" tests/fixtures/connections.example.json > tmp/connections.local.json
  echo "created tmp/connections.local.json, the connection list used by 'seed fixtures'"
fi
set_if_empty FIXTURE_CONNECTIONS_FILE "$root/tmp/connections.local.json"

echo ".env is ready; empty keys were filled with local random values. Do not commit or share it."
