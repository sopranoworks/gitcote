#!/bin/bash
# E2E Merge Conflict — Success Path
# Mock reviewer approves → auto-merge → conflict detected → real Claude merger
# resolves conflict on shared.go → PR merges.
#
# Requires: claude CLI + authentication (ANTHROPIC_API_KEY or mounted ~/.claude/)
#
# Usage (Docker):
#   docker run --rm \
#     -v "$(dirname $(pwd)):/work-src" \
#     -v "$HOME/.claude:/root/.claude:ro" \
#     -w /work-src/gitcote \
#     -e ANTHROPIC_API_KEY \
#     -e GOFLAGS=-buildvcs=false \
#     <image> ./scripts/e2e-merge-conflict-success.sh
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
        for f in /tmp/gitcote-agent-*; do
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

mkdir -p "$BUILD_DIR" "$DATA_DIR" "$AGENTS_DIR/slow_reviewer" "$CLONE_DIR"

NS="e2e"
PROJ="mergeconflict"
HTTP_PORT=19380
MCP_PORT=19381
MCP_OAUTH_PORT=19382

TIMEOUT=480
POLL_INTERVAL=5

echo "=== E2E Merge Conflict — Success Path ==="
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
go build -o "$BUILD_DIR/gitcote"       ./cmd/gitcote
go build -tags e2e -o "$BUILD_DIR/mock-reviewer" ./internal/e2e/testcmd/mock-reviewer
go build -tags e2e -o "$BUILD_DIR/e2e-helper"    ./internal/e2e/testcmd/e2e-helper
echo "built: gitcote, mock-reviewer, e2e-helper"

# ---- Step 2: Setup (repo + event settings) ----
# Use slow_reviewer (mock) for the review step, default_claude_merger (real) for conflict resolution.
echo ""
echo "--- Step 2: Setup ---"
"$BUILD_DIR/e2e-helper" --setup \
  --base-dir="$DATA_DIR" \
  --ns="$NS" \
  --proj="$PROJ" \
  --agent-name=slow_reviewer \
  --merger-agent=default_claude_merger

# ---- Step 3: Write reviewer agent config ----
echo ""
echo "--- Step 3: Agent config ---"
# Reviewer with 8-second delay to allow conflicting commit before approval
cat > "$AGENTS_DIR/slow_reviewer/agent.yaml" <<YAML
agent:
  role: reviewer
  display_name: "Slow Mock Reviewer (E2E)"
  command: 'sleep 8 && $BUILD_DIR/mock-reviewer'
  prompt: "Review and approve PR \$PR_ID"
YAML
echo "wrote slow_reviewer config (merger uses builtin default_claude_merger)"

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
  agents_root: "$AGENTS_DIR"
  default_timeout: "5m"
YAML
echo "wrote $E2E_DIR/gitcote.yaml"

# ---- Step 5: Start server ----
echo ""
echo "--- Step 5: Starting server ---"
"$BUILD_DIR/gitcote" --config "$E2E_DIR/gitcote.yaml" > "$LOG_DIR/server.log" 2>&1 &
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
  -o "pull_request.title=E2E merge conflict test (success)" 2>&1
echo "pushed feat/conflict-test with PR creation"
echo "(reviewer has 8s delay — writing conflict to main now)"

# ---- Step 8: Write conflicting commit directly to server's repo ----
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

# ---- Step 9: Wait for auto-merge (reviewer approve → conflict → merger resolve → merge) ----
echo ""
echo "--- Step 9: Waiting for auto-merge via conflict resolution (timeout ${TIMEOUT}s) ---"
echo "Expected: slow_reviewer approves → merge conflict → default_claude_merger resolves → PR merged"
MERGED=false
ELAPSED=0
while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
  git fetch origin main 2>/dev/null || true
  if git log origin/main --oneline 2>/dev/null | grep -q "Merge pull request #1"; then
    MERGED=true
    echo "PR merged via merge commit! (after ${ELAPSED}s)"
    break
  fi
  if git log origin/main --oneline 2>/dev/null | grep -q "feat:.*shared\|add feature\|Resolve merge"; then
    MERGED=true
    echo "PR merged! (after ${ELAPSED}s)"
    break
  fi
  if [ $((ELAPSED % 30)) -eq 0 ] && [ "$ELAPSED" -gt 0 ]; then
    echo "  still waiting... (${ELAPSED}/${TIMEOUT}s)"
    grep -iE "merger|conflict|approv|merg|reattempt" "$LOG_DIR/server.log" 2>/dev/null | tail -5 || true
  fi
  sleep "$POLL_INTERVAL"
  ELAPSED=$((ELAPSED + POLL_INTERVAL))
done

if [ "$MERGED" != "true" ]; then
  echo ""
  echo "agent log files:"
  ls -la /tmp/gitcote-agent-merger-* 2>/dev/null || echo "  (none)"
  for f in /tmp/gitcote-agent-merger-*; do
    [ -f "$f" ] && echo "--- $f ---" && cat "$f" 2>/dev/null
  done
  kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null || true
  SERVER_PID=0
  "$BUILD_DIR/e2e-helper" --check --base-dir="$DATA_DIR" --ns="$NS" --proj="$PROJ" 2>&1 || true
  fail "PR was not merged within ${TIMEOUT} seconds"
fi

# ---- Step 10: Verify git state ----
echo ""
echo "--- Step 10: Git state verification ---"
git checkout main 2>/dev/null
git pull origin main 2>&1
if [ -f feature.go ]; then
  echo "PASS: feature.go present on main"
else
  fail "feature.go not found on main"
fi

# ---- Step 11: Verify PR state in database ----
echo ""
echo "--- Step 11: PR state verification ---"
kill $SERVER_PID 2>/dev/null; wait $SERVER_PID 2>/dev/null || true
SERVER_PID=0
"$BUILD_DIR/e2e-helper" --check --base-dir="$DATA_DIR" --ns="$NS" --proj="$PROJ"

# ---- Step 12: Print summary ----
echo ""
echo "=== SERVER LOG (conflict + merger excerpts) ==="
grep -iE "merger|conflict|token|approv|merg|reattempt" "$LOG_DIR/server.log" 2>/dev/null | tail -50 || echo "(no matching lines)"

echo ""
echo "=== MERGER AGENT LOGS ==="
for f in /tmp/gitcote-agent-merger-*; do
  [ -f "$f" ] && echo "--- $f ---" && cat "$f" 2>/dev/null
done

echo ""
echo "=== E2E Merge Conflict — Success Path PASSED ==="
echo "Flow: push → PR created → reviewer approves → merge conflict → claude merger resolves → PR merged"
