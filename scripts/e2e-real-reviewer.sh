#!/bin/bash
# E2E Real Reviewer Test — real GitYard server + real Claude Code reviewer agent
#
# Usage (Docker):
#   docker run --rm \
#     -v "$(dirname $(pwd)):/work-src" \
#     -v "$HOME/.claude:/root/.claude:ro" \
#     -w /work-src/gityard \
#     -e ANTHROPIC_API_KEY \
#     -e GOFLAGS=-buildvcs=false \
#     <image> ./scripts/e2e-real-reviewer.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"

E2E_DIR=$(mktemp -d)
SERVER_PID=0
LOG_DIR="$E2E_DIR/logs"
mkdir -p "$LOG_DIR"

cleanup() {
    if [ $SERVER_PID -gt 0 ]; then
        kill $SERVER_PID 2>/dev/null || true
        wait $SERVER_PID 2>/dev/null || true
    fi
    if [ "${TEST_FAILED:-0}" = "1" ]; then
        echo ""
        echo "=== SERVER LOG ==="
        cat "$LOG_DIR/server.log" 2>/dev/null || echo "(no server log)"
        echo ""
        echo "=== AGENT LOGS ==="
        for f in /tmp/gityard-agent-*; do
            [ -f "$f" ] && echo "--- $f ---" && cat "$f" 2>/dev/null
        done
    fi
    rm -rf "$E2E_DIR"
}
trap cleanup EXIT

fail() {
    TEST_FAILED=1
    echo "FAIL: $1"
    exit 1
}

BUILD_DIR="$E2E_DIR/build"
DATA_DIR="$E2E_DIR/data"
CLONE_DIR="$E2E_DIR/clone"

mkdir -p "$BUILD_DIR" "$DATA_DIR" "$CLONE_DIR"

NS="e2e"
PROJ="realreview"
HTTP_PORT=19180
MCP_PORT=19181
MCP_OAUTH_PORT=19182

TIMEOUT=300
POLL_INTERVAL=5

echo "=== E2E Real Reviewer Test ==="
echo "temp dir: $E2E_DIR"

# ---- Pre-flight: check claude CLI ----
echo ""
echo "--- Pre-flight: Checking claude CLI ---"
if ! command -v claude &>/dev/null; then
    fail "claude CLI not found in PATH. Install with: npm install -g @anthropic-ai/claude-code"
fi
echo "claude found: $(which claude)"

# ---- Pre-flight: check authentication ----
echo ""
echo "--- Pre-flight: Checking authentication ---"
if [ -f "$HOME/.claude/.credentials.json" ]; then
    echo "auth: using mounted claude credentials (~/.claude/.credentials.json)"
elif [ -f "$HOME/.config/claude/credentials.json" ]; then
    echo "auth: using mounted claude credentials (~/.config/claude/credentials.json)"
elif [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    echo "auth: using ANTHROPIC_API_KEY"
else
    echo "ERROR: No claude authentication available."
    echo "Either mount ~/.claude/ (or ~/.config/claude/) or set ANTHROPIC_API_KEY."
    exit 1
fi

# ---- Step 1: Build binaries ----
echo ""
echo "--- Step 1: Building binaries ---"
cd "$REPO_DIR"
go build -o "$BUILD_DIR/gityard"    ./cmd/gityard
go build -o "$BUILD_DIR/e2e-helper" ./cmd/e2e-helper
echo "built: gityard, e2e-helper"

# ---- Step 2: Setup (repo + bbolt settings) ----
echo ""
echo "--- Step 2: Setup ---"
"$BUILD_DIR/e2e-helper" --setup \
  --base-dir="$DATA_DIR" \
  --ns="$NS" \
  --proj="$PROJ" \
  --agent-name=default_claude_reviewer

# ---- Step 3: Write server config ----
echo ""
echo "--- Step 3: Server config ---"
cat > "$E2E_DIR/gityard.yaml" <<YAML
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
    oauth:
      listen: "127.0.0.1:$MCP_OAUTH_PORT"
      external_url: "http://127.0.0.1:$MCP_OAUTH_PORT"
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
  default_timeout: "5m"
YAML
echo "wrote $E2E_DIR/gityard.yaml"

# ---- Step 4: Start server ----
echo ""
echo "--- Step 4: Starting server ---"
"$BUILD_DIR/gityard" --config "$E2E_DIR/gityard.yaml" > "$LOG_DIR/server.log" 2>&1 &
SERVER_PID=$!
echo "server PID=$SERVER_PID"

for i in $(seq 1 30); do
  if curl -s -o /dev/null "http://127.0.0.1:$HTTP_PORT/" 2>/dev/null; then
    echo "server ready (attempt $i)"
    break
  fi
  if ! kill -0 $SERVER_PID 2>/dev/null; then
    fail "server exited prematurely"
  fi
  sleep 0.5
done

if ! curl -s -o /dev/null "http://127.0.0.1:$HTTP_PORT/" 2>/dev/null; then
  fail "server did not become ready"
fi

# ---- Step 5: Initial commit on main ----
echo ""
echo "--- Step 5: Initial commit ---"
export GIT_AUTHOR_NAME="E2E Test"
export GIT_AUTHOR_EMAIL="e2e@test.local"
export GIT_COMMITTER_NAME="E2E Test"
export GIT_COMMITTER_EMAIL="e2e@test.local"

GIT_URL="http://127.0.0.1:$HTTP_PORT/$NS/$PROJ.git"
git clone "$GIT_URL" "$CLONE_DIR/repo" 2>&1
cd "$CLONE_DIR/repo"

echo "# E2E Real Reviewer Test" > README.md
git add README.md
git commit -m "initial commit"
git push -u origin HEAD:refs/heads/main 2>&1
echo "pushed initial commit to main"

# ---- Step 6: Push feature branch with PR options ----
echo ""
echo "--- Step 6: Feature branch + PR creation ---"
git checkout -b feat/e2e-real-review
cat > feature.go <<'GO'
package main

func hello() { println("hello from real reviewer e2e") }
GO
git add feature.go
git commit -m "add feature"
git push -u origin feat/e2e-real-review \
  -o pull_request.create \
  -o "pull_request.title=E2E real reviewer test" 2>&1
echo "pushed feat/e2e-real-review with PR creation"

# ---- Step 7: Wait for auto-merge ----
echo ""
echo "--- Step 7: Waiting for auto-merge (timeout ${TIMEOUT}s) ---"
MERGED=false
ELAPSED=0
while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
  git fetch origin main 2>/dev/null || true
  if git log origin/main --oneline 2>/dev/null | grep -q "Merge pull request #1"; then
    MERGED=true
    echo "PR merged! (after ${ELAPSED}s)"
    break
  fi
  if git log origin/main --oneline 2>/dev/null | grep -q "add feature"; then
    MERGED=true
    echo "PR merged (fast-forward)! (after ${ELAPSED}s)"
    break
  fi
  if [ $((ELAPSED % 30)) -eq 0 ] && [ "$ELAPSED" -gt 0 ]; then
    echo "  still waiting... (${ELAPSED}/${TIMEOUT}s)"
  fi
  sleep "$POLL_INTERVAL"
  ELAPSED=$((ELAPSED + POLL_INTERVAL))
done

if [ "$MERGED" != "true" ]; then
  echo ""
  echo "agent log files:"
  ls -la /tmp/gityard-agent-* 2>/dev/null || echo "  (none)"
  for f in /tmp/gityard-agent-*; do
    [ -f "$f" ] && echo "--- $f ---" && cat "$f" 2>/dev/null
  done
  kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null || true
  SERVER_PID=0
  "$BUILD_DIR/e2e-helper" --check --base-dir="$DATA_DIR" --ns="$NS" --proj="$PROJ" 2>&1 || true
  fail "PR was not merged within ${TIMEOUT} seconds"
fi

# ---- Step 8: Verify git state ----
echo ""
echo "--- Step 8: Git state verification ---"
git checkout main 2>/dev/null
git pull origin main 2>&1
if git log --oneline | grep -q "add feature"; then
  echo "PASS: feature commit present on main"
else
  fail "feature commit not found on main"
fi

# ---- Step 9: Verify PR state in database ----
echo ""
echo "--- Step 9: PR state verification ---"
kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null || true
SERVER_PID=0
"$BUILD_DIR/e2e-helper" --check --base-dir="$DATA_DIR" --ns="$NS" --proj="$PROJ"

# ---- Step 10: Print summary ----
echo ""
echo "=== SERVER LOG (agent spawn + MCP calls) ==="
grep -iE "agent|spawn|token|approv|merg|reject|mcp" "$LOG_DIR/server.log" 2>/dev/null | tail -50 || echo "(no matching lines)"

echo ""
echo "=== E2E Real Reviewer Test PASSED ==="
echo "Flow: push → PR created → real claude reviewer spawned → approved → auto-merged → main updated"
