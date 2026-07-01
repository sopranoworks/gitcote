#!/bin/bash
# E2E Merge Conflict — Failure Path
# Mock merger agent (exit 1) → PR enters interrupted state.
# No real Claude agent needed.
#
# Usage (Docker):
#   docker run --rm \
#     -v "$(dirname $(pwd)):/work-src" \
#     -w /work-src/gityard \
#     -e GOFLAGS=-buildvcs=false \
#     golang:1.26 ./scripts/e2e-merge-conflict-failure.sh
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
AGENTS_DIR="$E2E_DIR/agents"
CLONE_DIR="$E2E_DIR/clone"

mkdir -p "$BUILD_DIR" "$DATA_DIR" "$AGENTS_DIR/slow_reviewer" "$AGENTS_DIR/failing_merger" "$CLONE_DIR"

NS="e2e"
PROJ="mergeconflict"
HTTP_PORT=19280
MCP_PORT=19281

TIMEOUT=180
POLL_INTERVAL=3

echo "=== E2E Merge Conflict — Failure Path ==="
echo "temp dir: $E2E_DIR"

# ---- Step 1: Build binaries ----
echo ""
echo "--- Step 1: Building binaries ---"
cd "$REPO_DIR"
go build -o "$BUILD_DIR/gityard"       ./cmd/gityard
go build -o "$BUILD_DIR/mock-reviewer" ./cmd/mock-reviewer
go build -o "$BUILD_DIR/e2e-helper"    ./cmd/e2e-helper
echo "built: gityard, mock-reviewer, e2e-helper"

# ---- Step 2: Setup (repo + event settings) ----
echo ""
echo "--- Step 2: Setup ---"
"$BUILD_DIR/e2e-helper" --setup \
  --base-dir="$DATA_DIR" \
  --ns="$NS" \
  --proj="$PROJ" \
  --agent-name=slow_reviewer \
  --merger-agent=failing_merger

# ---- Step 3: Write agent configs ----
echo ""
echo "--- Step 3: Agent configs ---"
# Reviewer with 8-second delay to allow conflicting push to main before approval
cat > "$AGENTS_DIR/slow_reviewer/agent.yaml" <<YAML
agent:
  role: reviewer
  display_name: "Slow Mock Reviewer (E2E)"
  command: 'sleep 8 && $BUILD_DIR/mock-reviewer'
  prompt: "Review and approve PR \$PR_ID"
YAML

cat > "$AGENTS_DIR/failing_merger/agent.yaml" <<YAML
agent:
  role: merger
  display_name: "Failing Merger (E2E)"
  command: 'exit 1'
  prompt: "This merger always fails"
YAML
echo "wrote slow_reviewer + failing_merger configs"

# ---- Step 4: Write server config ----
echo ""
echo "--- Step 4: Server config ---"
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
echo "wrote $E2E_DIR/gityard.yaml"

# ---- Step 5: Start server ----
echo ""
echo "--- Step 5: Starting server ---"
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

# ---- Step 6: Initial commit on main with shared.go ----
echo ""
echo "--- Step 6: Initial commit with shared.go ---"
export GIT_AUTHOR_NAME="E2E Test"
export GIT_AUTHOR_EMAIL="e2e@test.local"
export GIT_COMMITTER_NAME="E2E Test"
export GIT_COMMITTER_EMAIL="e2e@test.local"

GIT_URL="http://127.0.0.1:$HTTP_PORT/$NS/$PROJ.git"
git clone "$GIT_URL" "$CLONE_DIR/repo" 2>&1
cd "$CLONE_DIR/repo"

cat > shared.go <<'GO'
package main

func Shared() string { return "original" }
GO
git add shared.go
git commit -m "initial: add shared.go"
git push -u origin HEAD:refs/heads/main 2>&1
echo "pushed initial commit to main"

# ---- Step 7: Create feature branch with conflicting change ----
echo ""
echo "--- Step 7: Feature branch with conflicting change ---"
git checkout -b feat/conflict-test
cat > shared.go <<'GO'
package main

func Shared() string { return "feature change" }
GO
cat > feature.go <<'GO'
package main

func Feature() string { return "new feature" }
GO
git add shared.go feature.go
git commit -m "feat: modify shared.go + add feature.go"
git push -u origin feat/conflict-test \
  -o pull_request.create \
  -o "pull_request.title=E2E merge conflict test (failure)" 2>&1
echo "pushed feat/conflict-test with PR creation"
echo "(reviewer has 8s delay — pushing conflict to main now)"

# ---- Step 8: Push conflicting commit to main (before reviewer approves) ----
# Write the conflicting commit directly to the server's repo directory.
# This bypasses the HTTP endpoint (which has a pre-receive timing issue with
# fast-forward checks on protected branches) and is the most reliable approach.
echo ""
echo "--- Step 8: Conflicting commit on main (direct repo write) ---"
REPO_DATA_DIR="$DATA_DIR/$NS/$PROJ"
cd "$REPO_DATA_DIR"
cat > shared.go <<'GO'
package main

func Shared() string { return "main change" }
GO
git add shared.go
git commit -m "main: modify shared.go (conflicting)" 2>&1
echo "committed conflicting change to main in server repo"
cd "$CLONE_DIR/repo"

# ---- Step 9: Wait for PR to reach interrupted state ----
echo ""
echo "--- Step 9: Waiting for interrupted state (timeout ${TIMEOUT}s) ---"
echo "Expected: reviewer approves → merge conflict → failing_merger exit 1 → interrupted"
DONE=false
ELAPSED=0
while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
  # Check server log for interrupted marker
  if grep -q "marked PR interrupted\|mark PR interrupted\|agent_spawn_failed" "$LOG_DIR/server.log" 2>/dev/null; then
    sleep 2
    DONE=true
    echo "PR marked interrupted (after ${ELAPSED}s)"
    break
  fi
  if [ $((ELAPSED % 15)) -eq 0 ] && [ "$ELAPSED" -gt 0 ]; then
    echo "  still waiting... (${ELAPSED}/${TIMEOUT}s)"
    grep -iE "agent|spawn|merger|interrupt|conflict|approv" "$LOG_DIR/server.log" 2>/dev/null | tail -5 || true
  fi
  sleep "$POLL_INTERVAL"
  ELAPSED=$((ELAPSED + POLL_INTERVAL))
done

if [ "$DONE" != "true" ]; then
  echo ""
  echo "Timeout — checking if server is still alive..."
  if ! kill -0 $SERVER_PID 2>/dev/null; then
    echo "server died"
  fi
  echo "agent log files:"
  ls -la /tmp/gityard-agent-* 2>/dev/null || echo "  (none)"
  for f in /tmp/gityard-agent-*; do
    [ -f "$f" ] && echo "--- $f ---" && cat "$f" 2>/dev/null
  done
fi

# ---- Step 10: Verify PR state ----
echo ""
echo "--- Step 10: PR state verification ---"
kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null || true
SERVER_PID=0
"$BUILD_DIR/e2e-helper" --check \
  --base-dir="$DATA_DIR" \
  --ns="$NS" \
  --proj="$PROJ" \
  --expect-state=interrupted

# ---- Step 11: Print summary ----
echo ""
echo "=== SERVER LOG (conflict + merger excerpts) ==="
grep -iE "agent|spawn|merger|conflict|interrupt|exit|approv|auto-confirm" "$LOG_DIR/server.log" 2>/dev/null | tail -30 || echo "(no matching lines)"

echo ""
echo "=== AGENT LOGS ==="
for f in /tmp/gityard-agent-*; do
  [ -f "$f" ] && echo "--- $f ---" && cat "$f" 2>/dev/null
done

echo ""
echo "=== E2E Merge Conflict — Failure Path PASSED ==="
echo "Flow: push → PR created → reviewer approves → merge conflict → failing_merger exit 1 → PR interrupted"
