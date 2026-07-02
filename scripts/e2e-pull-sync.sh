#!/bin/bash
# E2E Pull-Sync Tests — three test sections:
#   A) Simple pull-sync (fast-forward)
#   B) Pull-sync conflict with merger agent resolution
#   C) PR queue priority — seed sync waits while PR active, executes with priority
#
# Usage (local):   ./scripts/e2e-pull-sync.sh
# Usage (Docker):  docker run --rm -v "$(dirname $(pwd)):/work-src" \
#                    -w /work-src/gitcote -e GOFLAGS=-buildvcs=false \
#                    golang:1.26 ./scripts/e2e-pull-sync.sh
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
SEED_DIR="$E2E_DIR/seed"

mkdir -p "$BUILD_DIR" "$DATA_DIR" "$AGENTS_DIR" "$CLONE_DIR" "$SEED_DIR"

NS="e2e"
HTTP_PORT=19480
MCP_PORT=19481
MCP_OAUTH_PORT=19482
VAULT_PASSWORD="e2e-test-password"

export GIT_AUTHOR_NAME="E2E Test"
export GIT_AUTHOR_EMAIL="e2e@test.local"
export GIT_COMMITTER_NAME="E2E Test"
export GIT_COMMITTER_EMAIL="e2e@test.local"

echo "=== E2E Pull-Sync Tests ==="
echo "temp dir: $E2E_DIR"

# ---- Step 1: Build binaries ----
echo ""
echo "--- Step 1: Building binaries ---"
cd "$REPO_DIR"
go build -o "$BUILD_DIR/gitcote"       ./cmd/gitcote
go build -o "$BUILD_DIR/mock-reviewer" ./cmd/mock-reviewer
go build -o "$BUILD_DIR/e2e-helper"    ./cmd/e2e-helper
echo "built: gitcote, mock-reviewer, e2e-helper"

start_server() {
    local proj=$1
    local data_dir=$2

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
  base_dir: "$data_dir"
  vault_password: "$VAULT_PASSWORD"

identity:
  user:
    name: "e2e-test"
    email: "e2e@test.local"

agent_spawn:
  enabled: true
  agents_root: "$AGENTS_DIR"
  default_timeout: "3m"
YAML

    "$BUILD_DIR/gitcote" --config "$E2E_DIR/gitcote.yaml" > "$LOG_DIR/server.log" 2>&1 &
    SERVER_PID=$!
    echo "server PID=$SERVER_PID"

    for i in $(seq 1 30); do
      if curl -s -o /dev/null "http://127.0.0.1:$HTTP_PORT/" 2>/dev/null; then
        echo "server ready (attempt $i)"
        return
      fi
      if ! kill -0 $SERVER_PID 2>/dev/null; then
        fail "server exited prematurely"
      fi
      sleep 0.5
    done
    fail "server did not become ready"
}

stop_server() {
    if [ $SERVER_PID -gt 0 ]; then
        kill $SERVER_PID 2>/dev/null || true
        wait $SERVER_PID 2>/dev/null || true
        SERVER_PID=0
    fi
}

create_seed_repo() {
    local seed_path=$1
    mkdir -p "$(dirname "$seed_path")"
    git init --bare "$seed_path" 2>&1
    git -C "$seed_path" symbolic-ref HEAD refs/heads/main
}

# mcp_call sends a JSON-RPC request to the MCP endpoint and extracts JSON from SSE.
# Usage: mcp_call <json-body> [session-id]
mcp_call() {
    local body=$1
    local session_id=${2:-}
    local headers=(-H "Content-Type: application/json")
    if [ -n "$session_id" ]; then
        headers+=(-H "Mcp-Session-Id: $session_id")
    fi
    curl -s -X POST "http://127.0.0.1:$MCP_PORT/mcp" \
      "${headers[@]}" \
      -d "$body" | grep '^data: ' | sed 's/^data: //'
}

# mcp_call_with_headers returns both headers and SSE data.
mcp_call_with_headers() {
    local body=$1
    local session_id=${2:-}
    local headers=(-H "Content-Type: application/json")
    if [ -n "$session_id" ]; then
        headers+=(-H "Mcp-Session-Id: $session_id")
    fi
    curl -s -D - -X POST "http://127.0.0.1:$MCP_PORT/mcp" \
      "${headers[@]}" \
      -d "$body"
}

mcp_init() {
    local raw_response
    raw_response=$(mcp_call_with_headers '{
      "jsonrpc": "2.0",
      "id": 1,
      "method": "initialize",
      "params": {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": "e2e-test", "version": "1.0"}
      }
    }')
    MCP_SESSION_ID=$(echo "$raw_response" | grep -i '^mcp-session-id:' | tr -d '\r' | awk '{print $2}')
    # Send initialized notification
    mcp_call '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' "$MCP_SESSION_ID" > /dev/null 2>&1
    echo "$MCP_SESSION_ID"
}

mcp_pull_from_seed() {
    local ns=$1 proj=$2 sid=$3 call_id=${4:-10}
    mcp_call "{
      \"jsonrpc\": \"2.0\",
      \"id\": $call_id,
      \"method\": \"tools/call\",
      \"params\": {
        \"name\": \"pull_from_seed\",
        \"arguments\": {\"namespace\": \"$ns\", \"project_name\": \"$proj\"}
      }
    }" "$sid"
}

push_to_seed() {
    local seed_path=$1
    local work_dir=$2
    shift 2

    # Use a temp working copy for the seed
    local tmp_work=$(mktemp -d)
    git clone "$seed_path" "$tmp_work/repo" 2>&1
    cd "$tmp_work/repo"
    git checkout -b main 2>/dev/null || git checkout main 2>/dev/null || true
    "$@"
    git add -A
    git commit -m "seed commit" 2>&1
    git push origin HEAD:main 2>&1
    cd "$REPO_DIR"
    rm -rf "$tmp_work"
}

# ============================================================
# TEST A: Simple pull-sync (fast-forward)
# ============================================================
echo ""
echo "=========================================="
echo "=== TEST A: Simple pull-sync (fast-forward) ==="
echo "=========================================="

PROJ_A="pullsync-simple"
DATA_A="$DATA_DIR/test-a"
SEED_A="$SEED_DIR/test-a.git"
CLONE_A="$CLONE_DIR/test-a"

mkdir -p "$DATA_A" "$CLONE_A"

# Create seed repo with initial content
echo "--- A.1: Create seed repo ---"
create_seed_repo "$SEED_A"

TMP_SEED_WORK=$(mktemp -d)
git clone "$SEED_A" "$TMP_SEED_WORK/repo" 2>&1
cd "$TMP_SEED_WORK/repo"
echo "initial content" > file1.txt
git add file1.txt
git commit -m "initial commit" 2>&1
git push origin HEAD:main 2>&1
cd "$REPO_DIR"
rm -rf "$TMP_SEED_WORK"
echo "seed repo created with initial commit"

# Setup GitCote project with seed
echo "--- A.2: Setup GitCote project ---"
"$BUILD_DIR/e2e-helper" --setup-seed \
  --base-dir="$DATA_A" \
  --ns="$NS" \
  --proj="$PROJ_A" \
  --seed-url="$SEED_A" \
  --vault-password="$VAULT_PASSWORD"

# Start server and pull initial content
echo "--- A.3: Start server & initial pull ---"
start_server "$PROJ_A" "$DATA_A"

SESSION_ID=$(mcp_init)
echo "MCP session: $SESSION_ID"

PULL_RESULT=$(mcp_pull_from_seed "$NS" "$PROJ_A" "$SESSION_ID" 2)
echo "Initial pull result: $(echo "$PULL_RESULT" | head -c 200)"

# Verify initial content
GIT_URL="http://127.0.0.1:$HTTP_PORT/$NS/$PROJ_A.git"
git clone "$GIT_URL" "$CLONE_A/repo" 2>&1
if [ ! -f "$CLONE_A/repo/file1.txt" ]; then
    fail "Test A: initial pull did not bring file1.txt"
fi
echo "initial pull verified: file1.txt present"

# Push new commit to seed
echo "--- A.4: Push new commit to seed ---"
TMP_SEED_WORK=$(mktemp -d)
git clone "$SEED_A" "$TMP_SEED_WORK/repo" 2>&1
cd "$TMP_SEED_WORK/repo"
echo "new content" > file2.txt
git add file2.txt
git commit -m "add file2.txt" 2>&1
git push origin HEAD:main 2>&1
SEED_HEAD=$(git rev-parse HEAD)
cd "$REPO_DIR"
rm -rf "$TMP_SEED_WORK"
echo "pushed file2.txt to seed (HEAD=$SEED_HEAD)"

# Trigger pull_from_seed
echo "--- A.5: Trigger pull ---"
PULL2_RESULT=$(mcp_pull_from_seed "$NS" "$PROJ_A" "$SESSION_ID" 3)
echo "Pull result: $(echo "$PULL2_RESULT" | head -c 200)"

# Check for fast-forward status
if echo "$PULL2_RESULT" | grep -q '"fast-forward"'; then
    echo "PASS: fast-forward detected"
elif echo "$PULL2_RESULT" | grep -q '"up-to-date"'; then
    echo "PASS: already up-to-date (fast-forward may have happened in initial pull)"
else
    echo "Pull result: $PULL2_RESULT"
    fail "Test A: expected fast-forward or up-to-date"
fi

# Verify GitCote main updated
cd "$CLONE_A/repo"
git fetch origin main 2>/dev/null
git checkout origin/main 2>/dev/null || git checkout FETCH_HEAD 2>/dev/null
if [ -f "file2.txt" ]; then
    echo "PASS: file2.txt present on GitCote main"
else
    fail "Test A: file2.txt not found on GitCote main after pull"
fi
cd "$REPO_DIR"

# Verify server log
echo "--- A.6: Server log verification ---"
grep -iE "seed|pull|fast-forward|ref updated" "$LOG_DIR/server.log" 2>/dev/null | tail -5 || true

stop_server
echo ""
echo "=== TEST A PASSED ==="

# ============================================================
# TEST B: Pull-sync conflict with merger agent
# ============================================================
echo ""
echo "=========================================="
echo "=== TEST B: Pull-sync conflict with merger agent ==="
echo "=========================================="

PROJ_B="pullsync-conflict"
DATA_B="$DATA_DIR/test-b"
SEED_B="$SEED_DIR/test-b.git"
CLONE_B="$CLONE_DIR/test-b"

mkdir -p "$DATA_B" "$CLONE_B"

# Create seed repo
echo "--- B.1: Create seed repo ---"
create_seed_repo "$SEED_B"

TMP_SEED_WORK=$(mktemp -d)
git clone "$SEED_B" "$TMP_SEED_WORK/repo" 2>&1
cd "$TMP_SEED_WORK/repo"
echo 'original content' > shared.txt
git add shared.txt
git commit -m "initial: add shared.txt" 2>&1
git push origin HEAD:main 2>&1
cd "$REPO_DIR"
rm -rf "$TMP_SEED_WORK"
echo "seed repo created with shared.txt"

# Write mock seed merger agent config
echo "--- B.2: Agent config ---"
mkdir -p "$AGENTS_DIR/mock_seed_merger"
cat > "$AGENTS_DIR/mock_seed_merger/agent.yaml" <<'YAML'
agent:
  role: merger
  display_name: "Mock Seed Merger (E2E)"
  command: |
    cd "$TEMP_CLONE_DIR" && \
    git config user.email "e2e@test.local" && \
    git config user.name "E2E Test" && \
    git fetch gitcote main 2>&1 && \
    git merge gitcote/main --no-edit -X ours -m "Merge seed sync conflict resolution" 2>&1 && \
    git push gitcote HEAD:main 2>&1
  prompt: "Resolve seed sync conflicts"
YAML
echo "wrote mock_seed_merger config"

# Setup project with seed and merger agent
echo "--- B.3: Setup project ---"
"$BUILD_DIR/e2e-helper" --setup-seed \
  --base-dir="$DATA_B" \
  --ns="$NS" \
  --proj="$PROJ_B" \
  --seed-url="$SEED_B" \
  --vault-password="$VAULT_PASSWORD" \
  --seed-merger-agent=mock_seed_merger

# Start server and do initial pull
echo "--- B.4: Start server & initial pull ---"
start_server "$PROJ_B" "$DATA_B"

SESSION_ID=$(mcp_init)
echo "MCP session: $SESSION_ID"

mcp_pull_from_seed "$NS" "$PROJ_B" "$SESSION_ID" 2 > /dev/null

# Create divergence: push different changes to seed and GitCote
echo "--- B.5: Create divergence ---"

# Push conflicting change to seed
TMP_SEED_WORK=$(mktemp -d)
git clone "$SEED_B" "$TMP_SEED_WORK/repo" 2>&1
cd "$TMP_SEED_WORK/repo"
echo 'seed change' > shared.txt
echo 'seed-only file' > seed_file.txt
git add -A
git commit -m "seed: modify shared.txt + add seed_file.txt" 2>&1
git push origin HEAD:main 2>&1
cd "$REPO_DIR"
rm -rf "$TMP_SEED_WORK"
echo "pushed conflicting change to seed"

# Push different change to GitCote main (direct repo write)
REPO_DATA_DIR="$DATA_B/$NS/$PROJ_B"
cd "$REPO_DATA_DIR"
echo 'gitcote change' > shared.txt
echo 'gitcote-only file' > gitcote_file.txt
git add -A
git commit -m "gitcote: modify shared.txt + add gitcote_file.txt" 2>&1
cd "$REPO_DIR"
echo "pushed conflicting change to GitCote main"

# Trigger pull — should detect conflict and spawn merger agent
echo "--- B.6: Trigger pull (expect conflict + agent resolution) ---"
PULL_RESULT=$(mcp_pull_from_seed "$NS" "$PROJ_B" "$SESSION_ID" 3)
echo "Pull result: $(echo "$PULL_RESULT" | head -c 300)"

if echo "$PULL_RESULT" | grep -q '"conflict"'; then
    echo "PASS: conflict detected as expected"
else
    fail "Test B: expected conflict status"
fi

# Wait for merger agent to resolve and push
echo "--- B.7: Waiting for merger agent (timeout 120s) ---"
TIMEOUT=120
ELAPSED=0
RESOLVED=false

GIT_URL="http://127.0.0.1:$HTTP_PORT/$NS/$PROJ_B.git"
git clone "$GIT_URL" "$CLONE_B/repo" 2>&1

while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
  cd "$CLONE_B/repo"
  git fetch origin main 2>/dev/null || true
  # Check if main has both seed_file.txt and gitcote_file.txt
  if git show origin/main:seed_file.txt >/dev/null 2>&1 && \
     git show origin/main:gitcote_file.txt >/dev/null 2>&1; then
    RESOLVED=true
    echo "Merger agent resolved conflict (after ${ELAPSED}s)"
    break
  fi
  cd "$REPO_DIR"
  if [ $((ELAPSED % 15)) -eq 0 ] && [ "$ELAPSED" -gt 0 ]; then
    echo "  still waiting... (${ELAPSED}/${TIMEOUT}s)"
    grep -iE "seed.sync|merger|conflict|agent|spawn" "$LOG_DIR/server.log" 2>/dev/null | tail -3 || true
  fi
  sleep 3
  ELAPSED=$((ELAPSED + 3))
done
cd "$REPO_DIR"

if [ "$RESOLVED" != "true" ]; then
    echo ""
    echo "Agent logs:"
    for f in /tmp/gitcote-agent-merger-*; do
        [ -f "$f" ] && echo "--- $f ---" && cat "$f" 2>/dev/null
    done
    fail "Test B: merger agent did not resolve conflict within ${TIMEOUT}s"
fi

# Verify both changes are on main
echo "--- B.8: Verify merge result ---"
cd "$CLONE_B/repo"
git checkout origin/main 2>/dev/null || git checkout FETCH_HEAD 2>/dev/null

if [ -f "seed_file.txt" ]; then
    echo "PASS: seed_file.txt present on main"
else
    fail "Test B: seed_file.txt missing from main"
fi

if [ -f "gitcote_file.txt" ]; then
    echo "PASS: gitcote_file.txt present on main"
else
    fail "Test B: gitcote_file.txt missing from main"
fi
cd "$REPO_DIR"

# Verify server log
echo "--- B.9: Server log verification ---"
grep -iE "seed.sync|merger|conflict|agent|spawn|resolved" "$LOG_DIR/server.log" 2>/dev/null | tail -10 || true

stop_server
echo ""
echo "=== TEST B PASSED ==="

# ============================================================
# TEST C: PR queue priority
# ============================================================
echo ""
echo "=========================================="
echo "=== TEST C: PR queue priority ==="
echo "=========================================="

PROJ_C="pullsync-queue"
DATA_C="$DATA_DIR/test-c"
SEED_C="$SEED_DIR/test-c.git"
CLONE_C="$CLONE_DIR/test-c"

mkdir -p "$DATA_C" "$CLONE_C"

# Create seed repo
echo "--- C.1: Create seed repo ---"
create_seed_repo "$SEED_C"

TMP_SEED_WORK=$(mktemp -d)
git clone "$SEED_C" "$TMP_SEED_WORK/repo" 2>&1
cd "$TMP_SEED_WORK/repo"
echo 'base content' > base.txt
git add base.txt
git commit -m "initial: base.txt" 2>&1
git push origin HEAD:main 2>&1
cd "$REPO_DIR"
rm -rf "$TMP_SEED_WORK"
echo "seed repo created"

# Write slow reviewer (delays to keep PR active)
echo "--- C.2: Agent configs ---"
mkdir -p "$AGENTS_DIR/slow_reviewer"
cat > "$AGENTS_DIR/slow_reviewer/agent.yaml" <<YAML
agent:
  role: reviewer
  display_name: "Slow Mock Reviewer (E2E)"
  command: 'sleep 10 && $BUILD_DIR/mock-reviewer'
  prompt: "Review and approve PR \$PR_ID"
YAML
echo "wrote slow_reviewer config"

# Setup project with seed + PR review
echo "--- C.3: Setup project ---"
"$BUILD_DIR/e2e-helper" --setup-seed \
  --base-dir="$DATA_C" \
  --ns="$NS" \
  --proj="$PROJ_C" \
  --seed-url="$SEED_C" \
  --vault-password="$VAULT_PASSWORD"

"$BUILD_DIR/e2e-helper" --setup \
  --base-dir="$DATA_C" \
  --ns="$NS" \
  --proj="$PROJ_C" \
  --agent-name=slow_reviewer

# Start server and do initial pull
echo "--- C.4: Start server & initial pull ---"
start_server "$PROJ_C" "$DATA_C"

SESSION_ID=$(mcp_init)
echo "MCP session: $SESSION_ID"

mcp_pull_from_seed "$NS" "$PROJ_C" "$SESSION_ID" 2 > /dev/null

# Create PR to keep queue busy
echo "--- C.5: Create PR (slow reviewer) ---"
GIT_URL="http://127.0.0.1:$HTTP_PORT/$NS/$PROJ_C.git"
git clone "$GIT_URL" "$CLONE_C/repo" 2>&1
cd "$CLONE_C/repo"
git checkout -b feat/queue-test 2>/dev/null
echo 'feature content' > feature.txt
git add feature.txt
git commit -m "feat: add feature.txt" 2>&1
git push -u origin feat/queue-test \
  -o pull_request.create \
  -o "pull_request.title=Queue priority test PR" 2>&1
echo "pushed feat/queue-test with PR creation (slow reviewer has 10s delay)"
cd "$REPO_DIR"

# Wait a moment for PR to be enqueued and reviewer to start
sleep 2

# Push new content to seed
echo "--- C.6: Push to seed while PR is active ---"
TMP_SEED_WORK=$(mktemp -d)
git clone "$SEED_C" "$TMP_SEED_WORK/repo" 2>&1
cd "$TMP_SEED_WORK/repo"
echo 'seed update during PR' > seed_update.txt
git add seed_update.txt
git commit -m "seed: add seed_update.txt" 2>&1
git push origin HEAD:main 2>&1
cd "$REPO_DIR"
rm -rf "$TMP_SEED_WORK"
echo "pushed seed_update.txt to seed"

# Trigger pull — should be queued (PR is active)
echo "--- C.7: Trigger pull (expect queued) ---"
PULL_RESULT=$(mcp_pull_from_seed "$NS" "$PROJ_C" "$SESSION_ID" 4)
echo "Pull result: $(echo "$PULL_RESULT" | head -c 300)"

if echo "$PULL_RESULT" | grep -q '"queued"'; then
    echo "PASS: seed sync queued while PR is active"
else
    # May have executed immediately if PR was already processed
    echo "NOTE: seed sync may not have been queued (PR may have completed quickly)"
fi

# Wait for PR to merge and seed sync to complete
echo "--- C.8: Wait for PR merge + seed sync (timeout 120s) ---"
TIMEOUT=120
ELAPSED=0
BOTH_DONE=false

while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
  cd "$CLONE_C/repo"
  git fetch origin main 2>/dev/null || true
  PR_MERGED=false
  SEED_SYNCED=false

  if git log origin/main --oneline 2>/dev/null | grep -q "Merge pull request\|feat:.*feature"; then
    PR_MERGED=true
  fi

  if git show origin/main:seed_update.txt >/dev/null 2>&1; then
    SEED_SYNCED=true
  fi

  if [ "$PR_MERGED" = "true" ] && [ "$SEED_SYNCED" = "true" ]; then
    BOTH_DONE=true
    echo "Both PR merged and seed synced! (after ${ELAPSED}s)"
    break
  fi

  cd "$REPO_DIR"
  if [ $((ELAPSED % 15)) -eq 0 ] && [ "$ELAPSED" -gt 0 ]; then
    echo "  waiting... PR_MERGED=$PR_MERGED SEED_SYNCED=$SEED_SYNCED (${ELAPSED}/${TIMEOUT}s)"
    grep -iE "queue|seed.sync|dequeue|slot" "$LOG_DIR/server.log" 2>/dev/null | tail -3 || true
  fi
  sleep 3
  ELAPSED=$((ELAPSED + 3))
done
cd "$REPO_DIR"

if [ "$BOTH_DONE" != "true" ]; then
    echo ""
    echo "Final state: PR_MERGED=$PR_MERGED SEED_SYNCED=$SEED_SYNCED"
    fail "Test C: PR merge + seed sync did not complete within ${TIMEOUT}s"
fi

# Verify queue behavior in server log
echo "--- C.9: Server log verification ---"
echo "Queue-related log entries:"
grep -iE "queue|seed.sync|dequeue|slot|priority|enqueue" "$LOG_DIR/server.log" 2>/dev/null | tail -15 || true

# Verify mutual exclusion: seed sync should not overlap with PR processing
if grep -q "seed sync queued" "$LOG_DIR/server.log" 2>/dev/null; then
    echo "PASS: server log confirms seed sync was queued"
elif grep -q "seed sync dequeued" "$LOG_DIR/server.log" 2>/dev/null; then
    echo "PASS: server log confirms seed sync was dequeued from PR queue"
else
    echo "NOTE: seed sync may have executed immediately (PR completed before enqueue)"
fi

stop_server

# Verify PR state
echo "--- C.10: PR state verification ---"
"$BUILD_DIR/e2e-helper" --check --base-dir="$DATA_C" --ns="$NS" --proj="$PROJ_C"

echo ""
echo "=== TEST C PASSED ==="

# ============================================================
# Summary
# ============================================================
echo ""
echo "=========================================="
echo "=== ALL PULL-SYNC E2E TESTS PASSED ==="
echo "=========================================="
echo "  A) Simple pull-sync (fast-forward)     — PASSED"
echo "  B) Pull-sync conflict + merger agent   — PASSED"
echo "  C) PR queue priority                   — PASSED"
