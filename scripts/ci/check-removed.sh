#!/bin/sh
# Fail when code, file names or paths bring back the replaced architecture.
# Usage: scripts/ci/check-removed.sh [repository root]
set -eu
cd "${1:-$(dirname "$0")/../..}"

set -- -e 'html/template' -e '.gohtml' -e 'data-widget' -e 'mountWidget' -e 'internal/webui' \
  -e 'migration_drafts' -e 'migration_runs' -e 'migration-drafts' -e 'migration-runs' -e 'migration_ledger' \
  -e 'MIGRATION_TIMEOUT' -e 'Idempotency-Key' -e 'RequirePage' -e 'RedirectToLogin'
dirs=
for dir in backend web/src tests scripts infra; do
  [ -d "$dir" ] && dirs="$dirs $dir"
done
[ -n "$dirs" ] || { echo "no source directories found" >&2; exit 2; }

# The only files allowed to name old tables: the frozen 0004, the 0006 that drops them, and the
# initializer (with its tests) that must recognise an old ledger to reject it.
allowed='backend/migrations/0004_migrations.sql:
backend/migrations/0006_migration_workflow.sql:
backend/internal/migrations/history_init.go:
backend/internal/migrations/history_init_test.go:
backend/internal/platform/migrate_test.go:'

# shellcheck disable=SC2086 # dirs is a word list
contents=$(grep -rnIF --exclude-dir=node_modules --exclude=check-removed.sh --exclude=test_check_removed.py "$@" $dirs |
  grep -vF "$allowed" || [ $? -eq 1 ])
# shellcheck disable=SC2086
paths=$(find $dirs -name node_modules -prune -o -type f -print | grep -F "$@" || [ $? -eq 1 ])

if [ -n "$contents$paths" ]; then
  echo "Replaced architecture is still referenced:" >&2
  [ -z "$contents" ] || echo "$contents" >&2
  [ -z "$paths" ] || echo "$paths" >&2
  exit 1
fi
echo "No replaced architecture found."
