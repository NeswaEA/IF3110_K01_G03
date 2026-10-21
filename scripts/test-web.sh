#!/bin/sh
# Install from the lockfile, typecheck, test and build the frontend with the pinned Node 24.22.0 image.
# The whole repo is mounted because web/ imports the shared fixtures in tests/fixtures/api.
set -eu
cd "$(dirname "$0")/.."
src=$(pwd -W 2>/dev/null || pwd) # Git Bash on Windows needs the Windows path for -v
MSYS_NO_PATHCONV=1 exec docker run --rm -v "$src:/repo" -v if3110-web-node-modules:/repo/web/node_modules -w /repo/web node:24.22.0 \
  sh -c 'npm ci && npm run lint && npm test && npm run build'
