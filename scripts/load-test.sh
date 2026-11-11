#!/bin/sh
# Stress test with k6 against the running app (scripts/app-up.sh seed), results to Prometheus/Grafana.
# Prepares the load_1..load_4 tables first (seed load). Extra arguments go to `k6 run`.
# Usage: EDITOR_PASSWORD=... [LEADER_PASSWORD=...] scripts/load-test.sh [-e PROFILE=smoke] [-e TARGETS=same]
set -eu
cd "$(dirname "$0")/.."
[ -f .env ] || { echo "missing .env: run scripts/init-env.sh" >&2; exit 1; }
: "${EDITOR_PASSWORD:?set EDITOR_PASSWORD (the fixture users share FIXTURE_PASSWORD)}"
compose() { MSYS_NO_PATHCONV=1 docker compose --env-file .env -f infra/docker/compose.yml "$@"; }

compose --profile app run --rm --no-deps --entrypoint /app/seed backend load
compose --profile load up -d prometheus
compose --profile load run --rm \
  -e EDITOR_PASSWORD="$EDITOR_PASSWORD" -e LEADER_PASSWORD="${LEADER_PASSWORD:-$EDITOR_PASSWORD}" \
  k6 run -o experimental-prometheus-rw "$@" /scripts/stress.js
