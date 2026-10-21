#!/bin/sh
# Run go vet and all backend tests in the pinned Go 1.27.2 image (CGO is needed for SQLite and pg_query_go).
# Integration tests use TEST_METADATA_DATABASE_URL and TEST_TARGET_DATABASE_URL from .env;
# without them they skip with a message (with CI set they fail instead).
# Inside the container, the Compose databases are reachable through host.docker.internal.
# Extra arguments go to `go test`, e.g. scripts/test-backend.sh -run Fingerprint ./internal/database/
set -eu
cd "$(dirname "$0")/.."
if [ -f .env ]; then
  # only the two test DSNs are read from .env
  TEST_METADATA_DATABASE_URL=${TEST_METADATA_DATABASE_URL:-$(sed -n 's/^TEST_METADATA_DATABASE_URL=//p' .env)}
  TEST_TARGET_DATABASE_URL=${TEST_TARGET_DATABASE_URL:-$(sed -n 's/^TEST_TARGET_DATABASE_URL=//p' .env)}
fi
src=$(pwd -W 2>/dev/null || pwd) # Git Bash on Windows needs the Windows path for -v
[ "$#" -gt 0 ] || set -- ./...
MSYS_NO_PATHCONV=1 exec docker run --rm \
  -e GOTOOLCHAIN=local -e CI="${CI:-}" \
  -e TEST_METADATA_DATABASE_URL="${TEST_METADATA_DATABASE_URL:-}" \
  -e TEST_TARGET_DATABASE_URL="${TEST_TARGET_DATABASE_URL:-}" \
  -v if3110-gomodcache:/go/pkg/mod -v if3110-gobuildcache:/root/.cache/go-build \
  -v "$src:/src" -w /src/backend golang:1.27.2@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8 \
  sh -c 'test -z "$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed" >&2; exit 1; }; go vet ./... && go test -count=1 "$@"' sh "$@"
