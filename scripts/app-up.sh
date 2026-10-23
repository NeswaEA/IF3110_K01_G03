#!/bin/sh
# Build the app image, apply the metadata migrations and start the app at http://localhost:8080.
# Needs .env (scripts/init-env.sh) and the databases (scripts/dev-up.sh).
# Usage: scripts/app-up.sh        build, migrate, start
#        scripts/app-up.sh seed   also load the demo users, groups, grants and connections,
#                                 and create demo.sqlite in the SQLite volume if it is missing
set -eu
cd "$(dirname "$0")/.."
[ -f .env ] || { echo "missing .env: run scripts/init-env.sh" >&2; exit 1; }
src=$(pwd -W 2>/dev/null || pwd) # Git Bash on Windows needs the Windows path for -v
# MSYS_NO_PATHCONV stops Git Bash from rewriting /app/... into a Windows path
compose() { MSYS_NO_PATHCONV=1 docker compose --env-file .env -f infra/docker/compose.yml --profile app "$@"; }
# same pinned Node image as scripts/test-web.sh; its built-in node:sqlite loads the fixture without a sqlite3 CLI
node_image=node:24.22.0@sha256:14078204b6bb268c107ceff6d3c43f45a888105e5c2b18b14c32b3409d93a1b0

compose build backend
compose run --rm --entrypoint /app/migrate backend up
if [ "${1:-}" = seed ]; then
  [ -f tmp/connections.local.json ] || { echo "missing tmp/connections.local.json: run scripts/init-env.sh" >&2; exit 1; }
  # the app reaches the target database by its Compose service name, not the host port
  sed -e 's/"host": "localhost"/"host": "target-postgres"/' -e 's/"port": 15433/"port": 5432/' \
    tmp/connections.local.json > tmp/connections.compose.json
  compose run --rm --entrypoint /app/seed -v "$src/tmp:/fixtures:ro" \
    -e FIXTURE_CONNECTIONS_FILE=/fixtures/connections.compose.json backend fixtures
  # An existing demo.sqlite keeps its data and migration history; delete the volume to start over.
  # The file is built under a temporary name so a failed load never leaves a half-filled database,
  # and handed to uid 10001, the user the app container runs as.
  MSYS_NO_PATHCONV=1 docker run --rm -v "${COMPOSE_PROJECT_NAME:-if3110}_sqlite-data:/data" \
    -v "$src/tests/fixtures/sqlite:/fixture:ro" "$node_image" node -e '
      const fs = require("node:fs");
      const { DatabaseSync } = require("node:sqlite");
      const file = "/data/demo.sqlite", tmp = "/data/.demo.sqlite.tmp";
      if (fs.existsSync(file)) {
        console.log("demo.sqlite already exists; kept as is");
      } else {
        fs.rmSync(tmp, { force: true });
        try {
          const db = new DatabaseSync(tmp);
          db.exec("PRAGMA foreign_keys = ON");
          db.exec(fs.readFileSync("/fixture/demo.sql", "utf8"));
          db.close();
        } catch (err) {
          fs.rmSync(tmp, { force: true });
          throw err;
        }
        fs.renameSync(tmp, file);
        console.log("created demo.sqlite from tests/fixtures/sqlite/demo.sql");
      }
      fs.chownSync("/data", 10001, 10001);
      fs.chownSync(file, 10001, 10001);
    '
fi
compose up -d --wait backend
echo "app is running at http://localhost:8080"
