#!/usr/bin/env bash
# Run Playwright E2E tests inside the official Playwright Docker image.
# Usage: ./scripts/e2e-docker.sh [playwright test args...]
# Called by: npm run test:e2e (after the vite build step)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WEB_DIR="$(dirname "$SCRIPT_DIR")"
REPO_DIR="$(dirname "$WEB_DIR")"
# Mount the parent of the repo so go.mod replace ../shoka/pkg resolves correctly
SRC_DIR="$(dirname "$REPO_DIR")"
REPO_NAME="$(basename "$REPO_DIR")"

PW_VERSION=$(node -e "console.log(require('$WEB_DIR/node_modules/@playwright/test/package.json').version)")
IMAGE="mcr.microsoft.com/playwright:v${PW_VERSION}-noble"

GOROOT="$(go env GOROOT 2>/dev/null || true)"
if [ -z "$GOROOT" ]; then
  echo "error: go not found — the E2E global-setup builds the server binary" >&2
  exit 1
fi
GO_PKG_DIR="$(dirname "$(dirname "$GOROOT")")"

exec docker run --rm \
  --ipc=host \
  --network=host \
  -v "$SRC_DIR:/work-src" \
  -v "$GOROOT:$GOROOT:ro" \
  -v "$GO_PKG_DIR:$GO_PKG_DIR:ro" \
  -v "${GOPATH:-$HOME/go}:/root/go" \
  -v "${HOME}/.cache/go-build:/root/.cache/go-build" \
  -w "/work-src/$REPO_NAME/web" \
  -e "PATH=$GOROOT/bin:/usr/local/bin:/usr/bin:/bin" \
  -e "GOROOT=$GOROOT" \
  -e "GOPATH=/root/go" \
  -e "GOFLAGS=-buildvcs=false" \
  -e "HOME=/root" \
  -e "GITCOTE_E2E_PORT=${GITCOTE_E2E_PORT:-9099}" \
  "$IMAGE" \
  npx playwright test "$@"
