#!/bin/sh
# Start the observability stack (OTel Collector, Loki, Tempo, Prometheus, Grafana) and restart the
# API and workers so their traces and logs also go to the collector (stdout logs stay).
# Needs a running app (scripts/app-up.sh). Grafana: http://localhost:3000, Prometheus: http://localhost:9090
# Usage: scripts/obs-up.sh        start
#        scripts/obs-up.sh down   stop the stack (its volumes are kept) and return the app to console traces
set -eu
cd "$(dirname "$0")/.."
[ -f .env ] || { echo "missing .env: run scripts/init-env.sh" >&2; exit 1; }
compose() { MSYS_NO_PATHCONV=1 docker compose --env-file .env -f infra/docker/compose.yml "$@"; }
stack="otel-collector loki tempo prometheus grafana"

if [ "${1:-}" = down ]; then
  # shellcheck disable=SC2086
  compose --profile observability stop $stack
  OTEL_TRACES_EXPORTER=console OTEL_LOGS_EXPORTER=none compose --profile app up -d --no-build backend worker
  exit 0
fi
# shellcheck disable=SC2086
compose --profile observability up -d $stack
OTEL_TRACES_EXPORTER=otlp OTEL_LOGS_EXPORTER=otlp compose --profile app up -d --no-build backend worker
echo "Grafana: http://localhost:3000  Prometheus: http://localhost:9090"
