#!/bin/bash
# E2E Full Flow Test — runs the complete PR lifecycle inside a single environment:
#   server start → push → PR created → reviewer agent spawns → approve → auto-merge
#
# Usage (local):   ./scripts/e2e-full-flow.sh
# Usage (Docker):  docker run --rm -v "$(dirname $(pwd)):/work-src" \
#                    -w /work-src/gitcote -e GOFLAGS=-buildvcs=false \
#                    golang:1.26 ./scripts/e2e-full-flow.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"

E2E_DIR=$(mktemp -d)
SERVER_PID=0
trap '[ $SERVER_PID -gt 0 ] && kill $SERVER_PID 2>/dev/null; rm -rf "$E2E_DIR"' EXIT

BUILD_DIR="$E2E_DIR/build"
DATA_DIR="$E2E_DIR/data"
AGENTS_DIR="$E2E_DIR/agents"
CLONE_DIR="$E2E_DIR/clone"

mkdir -p "$BUILD_DIR" "$DATA_DIR" "$AGENTS_DIR/mock_reviewer" "$CLONE_DIR"

NS="e2e"
PROJ="fullflow"
HTTP_PORT=19080
MCP_PORT=19081

echo "=== E2E Full Flow Test ==="
echo "temp dir: $E2E_DIR"

# ---- Step 1: Build binaries ----
echo ""
echo "--- Step 1: Building binaries ---"
cd "$REPO_DIR"
go build -o "$BUILD_DIR/gitcote"        ./cmd/gitcote
go build -tags e2e -o "$BUILD_DIR/mock-reviewer"  ./internal/e2e/testcmd/mock-reviewer
go build -tags e2e -o "$BUILD_DIR/e2e-helper"     ./internal/e2e/testcmd/e2e-helper
echo "built: gitcote, mock-reviewer, e2e-helper"

# ---- Step 2: Setup (repo + bbolt settings) ----
echo ""
echo "--- Step 2: Setup ---"
"$BUILD_DIR/e2e-helper" --setup \
  --base-dir="$DATA_DIR" \
  --ns="$NS" \
  --proj="$PROJ" \
  --agent-name=mock_reviewer

# ---- Step 3: Write agent config ----
echo ""
echo "--- Step 3: Agent config ---"
cat > "$AGENTS_DIR/mock_reviewer/agent.yaml" <<YAML
agent:
  role: reviewer
  display_name: "Mock Reviewer (E2E)"
  command: '$BUILD_DIR/mock-reviewer'
  prompt: "Review and approve PR \$PR_ID"
YAML
echo "wrote $AGENTS_DIR/mock_reviewer/agent.yaml"

# ---- Step 4: Write server config ----
echo ""
echo "--- Step 4: Server config ---"
cat > "$E2E_DIR/gitcote.yaml" <<YAML
server:
  http:
    listen: "127.0.0.1:$HTTP_PORT"
    external_url: "http://127.0.0.1:$HTTP_PORT"
    trusted_networks:
      - "127.0.0.0/8"
  mcp:
    plain:
      listen: "127.0.0.1:$MCP_PORT"
      external_url: "http://127.0.0.1:$MCP_PORT"
      bearer_auth: false
  auth:
    enabled: false
    users:
      allow_first_run_admin: true

storage:
  base_dir: "$DATA_DIR"

identity:
  user:
    name: "e2e-test"
    email: "e2e@test.local"

agent_spawn:
  enabled: true
  agents_root: "$AGENTS_DIR"
  default_timeout: "2m"
YAML
echo "wrote $E2E_DIR/gitcote.yaml"

# ---- Step 5: Start server ----
echo ""
echo "--- Step 5: Starting server ---"
"$BUILD_DIR/gitcote" --config "$E2E_DIR/gitcote.yaml" &
SERVER_PID=$!
echo "server PID=$SERVER_PID"

# Wait for HTTP to be ready
for i in $(seq 1 30); do
  if curl -s -o /dev/null "http://127.0.0.1:$HTTP_PORT/" 2>/dev/null; then
    echo "server ready (attempt $i)"
    break
  fi
  if ! kill -0 $SERVER_PID 2>/dev/null; then
    echo "FAIL: server exited prematurely"
    exit 1
  fi
  sleep 0.5
done

if ! curl -s -o /dev/null "http://127.0.0.1:$HTTP_PORT/" 2>/dev/null; then
  echo "FAIL: server did not become ready"
  exit 1
fi

# ---- Step 6: Initial commit on main ----
echo ""
echo "--- Step 6: Initial commit ---"
export GIT_AUTHOR_NAME="E2E Test"
export GIT_AUTHOR_EMAIL="e2e@test.local"
export GIT_COMMITTER_NAME="E2E Test"
export GIT_COMMITTER_EMAIL="e2e@test.local"

GIT_URL="http://127.0.0.1:$HTTP_PORT/$NS/$PROJ.git"
git clone "$GIT_URL" "$CLONE_DIR/repo" 2>&1
cd "$CLONE_DIR/repo"

echo "# E2E Test Project" > README.md
git add README.md
git commit -m "initial commit"
git push -u origin HEAD:refs/heads/main 2>&1
echo "pushed initial commit to main"

# ---- Step 7: Push feature branch with PR options ----
echo ""
echo "--- Step 7: Feature branch + PR creation ---"
git checkout -b feat/e2e-test
echo "package main" > feature.go
echo "" >> feature.go
echo 'func hello() { println("hello from e2e") }' >> feature.go
git add feature.go
git commit -m "add feature"
git push -u origin feat/e2e-test \
  -o pull_request.create \
  -o "pull_request.title=E2E full flow test" 2>&1
echo "pushed feat/e2e-test with PR creation"

# ---- Step 8: Wait for auto-merge ----
echo ""
echo "--- Step 8: Waiting for auto-merge ---"
MERGED=false
for i in $(seq 1 120); do
  git fetch origin main 2>/dev/null || true
  if git log origin/main --oneline 2>/dev/null | grep -q "Merge pull request #1"; then
    MERGED=true
    echo "PR merged! (detected at attempt $i)"
    break
  fi
  # Also check for fast-forward (empty target case)
  if git log origin/main --oneline 2>/dev/null | grep -q "add feature"; then
    MERGED=true
    echo "PR merged (fast-forward)! (detected at attempt $i)"
    break
  fi
  if [ $((i % 10)) -eq 0 ]; then
    echo "  still waiting... (attempt $i/120)"
  fi
  sleep 1
done

if [ "$MERGED" != "true" ]; then
  echo ""
  echo "FAIL: PR was not merged within 120 seconds"
  echo "agent log files:"
  ls -la /tmp/gitcote-agent-* 2>/dev/null || echo "  (none)"
  for f in /tmp/gitcote-agent-*; do
    [ -f "$f" ] && echo "--- $f ---" && cat "$f" 2>/dev/null
  done
  # Stop server so bbolt check can open the database
  kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null || true
  "$BUILD_DIR/e2e-helper" --check --base-dir="$DATA_DIR" --ns="$NS" --proj="$PROJ" 2>&1 || true
  exit 1
fi

# ---- Step 9: Verify git state ----
echo ""
echo "--- Step 9: Git state verification ---"
git checkout main 2>/dev/null
git pull origin main 2>&1
if git log --oneline | grep -q "add feature"; then
  echo "PASS: feature commit present on main"
else
  echo "FAIL: feature commit not found on main"
  kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null || true
  exit 1
fi

# ---- Step 10: Verify PR state in database ----
echo ""
echo "--- Step 10: PR state verification ---"
kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null || true
SERVER_PID=0
"$BUILD_DIR/e2e-helper" --check --base-dir="$DATA_DIR" --ns="$NS" --proj="$PROJ"

echo ""
echo "=== E2E Full Flow Test PASSED ==="
echo "Flow: push → PR created → reviewer spawned → approved → auto-merged → main updated"
