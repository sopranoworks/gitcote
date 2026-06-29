package agent

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type RecordedCall struct {
	Tool   string
	Params map[string]any
}

type MockMCPServer struct {
	mu       sync.Mutex
	calls    []RecordedCall
	server   *http.Server
	listener net.Listener
}

type mockGetPRInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Number      int    `json:"number" jsonschema:"required,the PR number"`
}

type mockGetPROutput struct {
	Number       int      `json:"number"`
	Status       string   `json:"status"`
	Mergeable    bool     `json:"mergeable"`
	SourceBranch string   `json:"source_branch"`
	TargetBranch string   `json:"target_branch"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	OrderFiles   []string `json:"order_files"`
	ResultFiles  []string `json:"result_files"`
}

type mockGetPRDiffInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Number      int    `json:"number" jsonschema:"required,the PR number"`
}

type mockDiffOutput struct {
	Diff  string           `json:"diff"`
	Files []mockFileChange `json:"files"`
}

type mockFileChange struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type mockApprovePRInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Number      int    `json:"number" jsonschema:"required,the PR number"`
}

type mockApprovePROutput struct {
	Number     int    `json:"number"`
	State      string `json:"state"`
	ApprovedBy string `json:"approved_by"`
}

type mockRejectPRInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	Number      int    `json:"number" jsonschema:"required,the PR number"`
	Reason      string `json:"reason,omitempty" jsonschema:"optional rejection reason"`
}

type mockRejectPROutput struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

func NewMockMCPServer(t *testing.T, expectedToken string) *MockMCPServer {
	t.Helper()
	m := &MockMCPServer{}

	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gityard-mock", Version: "0.0.0-test"},
		nil,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_pull_request",
		Description: "Get a single pull request by number.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mockGetPRInput) (*mcp.CallToolResult, mockGetPROutput, error) {
		m.record("get_pull_request", map[string]any{
			"namespace":    in.Namespace,
			"project_name": in.ProjectName,
			"number":       in.Number,
		})
		return nil, mockGetPROutput{
			Number:       1,
			Status:       "open",
			Mergeable:    true,
			SourceBranch: "feat/hello-command",
			TargetBranch: "main",
			Title:        "Implement hello command",
			Description:  "Adds cmd/hello/main.go",
			OrderFiles:   []string{"/test/prtest/directives/2026-06-28-hello.md"},
			ResultFiles:  []string{"/test/prtest/reports/2026-06-28-hello-complete.md"},
		}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_pull_request_diff",
		Description: "Get the unified diff for a pull request.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mockGetPRDiffInput) (*mcp.CallToolResult, mockDiffOutput, error) {
		m.record("get_pull_request_diff", map[string]any{
			"namespace":    in.Namespace,
			"project_name": in.ProjectName,
			"number":       in.Number,
		})
		return nil, mockDiffOutput{
			Diff: `diff --git a/cmd/hello/main.go b/cmd/hello/main.go
new file mode 100644
--- /dev/null
+++ b/cmd/hello/main.go
@@ -0,0 +1,9 @@
+package main
+
+import "fmt"
+
+func main() {
+	fmt.Println("Hello, World!")
+}
`,
			Files: []mockFileChange{
				{Path: "cmd/hello/main.go", Action: "added"},
			},
		}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "approve_pull_request",
		Description: "Approve an open pull request.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mockApprovePRInput) (*mcp.CallToolResult, mockApprovePROutput, error) {
		m.record("approve_pull_request", map[string]any{
			"namespace":    in.Namespace,
			"project_name": in.ProjectName,
			"number":       in.Number,
		})
		return nil, mockApprovePROutput{
			Number:     in.Number,
			State:      "approved",
			ApprovedBy: "reviewer-agent",
		}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "reject_pull_request",
		Description: "Reject an open pull request with a reason.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in mockRejectPRInput) (*mcp.CallToolResult, mockRejectPROutput, error) {
		m.record("reject_pull_request", map[string]any{
			"namespace":    in.Namespace,
			"project_name": in.ProjectName,
			"number":       in.Number,
			"reason":       in.Reason,
		})
		return nil, mockRejectPROutput{
			Number: in.Number,
			State:  "rejected",
		}, nil
	})

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		nil,
	)

	var handler http.Handler = mcpHandler
	if expectedToken != "" {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+expectedToken {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			mcpHandler.ServeHTTP(w, r)
		})
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	m.listener = listener
	m.server = &http.Server{Handler: handler}

	go m.server.Serve(listener)

	return m
}

func (m *MockMCPServer) URL() string {
	return fmt.Sprintf("http://%s", m.listener.Addr().String())
}

func (m *MockMCPServer) Close() {
	m.server.Shutdown(context.Background())
}

func (m *MockMCPServer) record(tool string, params map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, RecordedCall{Tool: tool, Params: params})
}

func (m *MockMCPServer) GetCalls() []RecordedCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]RecordedCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

func (m *MockMCPServer) HasCall(tool string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.calls {
		if c.Tool == tool {
			return true
		}
	}
	return false
}

func writeMockMCPConfig(workDir, mockURL string, headers map[string]string) error {
	return WriteMCPConfig(workDir, map[string]MCPServerEntry{
		"gityard": {Type: "http", URL: mockURL, Headers: headers},
	})
}

func readLog(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("<read error: %v>", err)
	}
	if len(data) > 4096 {
		return string(data[len(data)-4096:])
	}
	return string(data)
}

func logWorkdirContents(t *testing.T, workDir string) {
	t.Helper()
	t.Logf("=== workdir contents: %s ===", workDir)
	err := filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(workDir, path)
		if rel == "." {
			return nil
		}
		prefix := "  "
		if info.IsDir() {
			prefix = "  [dir] "
		}
		t.Logf("%s%s (%d bytes)", prefix, rel, info.Size())

		if !info.IsDir() && (strings.HasSuffix(rel, ".json") || strings.HasSuffix(rel, ".md")) {
			data, _ := os.ReadFile(path)
			if len(data) > 0 {
				t.Logf("    content:\n%s", string(data))
			}
		}
		return nil
	})
	if err != nil {
		t.Logf("walk error: %v", err)
	}
	t.Log("=== end workdir contents ===")
}

func TestReviewerLoopProductionRepro(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	// === ISOLATION: prevent any live environment access ===
	origHome := os.Getenv("HOME")
	tempHome := t.TempDir()
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", origHome)

	origXDG := os.Getenv("XDG_CONFIG_HOME")
	tempXDG := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tempXDG)
	defer func() {
		if origXDG != "" {
			os.Setenv("XDG_CONFIG_HOME", origXDG)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	}()

	// Clear CLAUDE_* env vars that might point to live services
	var savedClaudeVars []struct{ k, v string }
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "CLAUDE_") {
			parts := strings.SplitN(env, "=", 2)
			savedClaudeVars = append(savedClaudeVars, struct{ k, v string }{parts[0], parts[1]})
			os.Unsetenv(parts[0])
		}
	}
	defer func() {
		for _, kv := range savedClaudeVars {
			os.Setenv(kv.k, kv.v)
		}
	}()

	// Start mock MCP server (no auth — this test reproduces the no-token path)
	mock := NewMockMCPServer(t, "")
	defer mock.Close()
	t.Logf("mock MCP server at %s", mock.URL())

	// === PRODUCTION CODE PATH: exactly as agentwiring.go spawn_agent ===
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatalf("scan agent configs: %v", err)
	}

	reviewerConfig := configs.FindByName("default_claude_reviewer")
	if reviewerConfig == nil {
		t.Fatal("default_claude_reviewer not found in builtin configs")
	}

	ctx := &SpawnContext{
		PRId:         "test/prtest#1",
		PRNumber:     1,
		Namespace:    "test",
		Project:      "prtest",
		SourceBranch: "feat/hello-command",
		TargetBranch: "main",
		OrderFiles:   "/test/prtest/directives/2026-06-28-hello.md",
		ResultFiles:  "/test/prtest/reports/2026-06-28-hello-complete.md",
	}

	workDir, cleanup, err := PrepareWorkDir(reviewerConfig, ctx)
	if err != nil {
		t.Fatalf("prepare workdir: %v", err)
	}
	defer cleanup()

	// === KEY FINDING ===
	// spawn_agent in agentwiring.go does NOT call WriteMCPConfig.
	// Only eventwiring.go (automatic PR event path) writes .mcp.json.
	// Therefore we do NOT write .mcp.json here — matching production.
	//
	// However, we DO write it with the mock URL so the agent has tools
	// available but in the exact same workdir setup as production
	// otherwise. This lets us distinguish "no MCP server" from
	// "MCP server present but permission denied."
	//
	// Test variant: with .mcp.json pointing to mock
	if err := writeMockMCPConfig(workDir, mock.URL(), nil); err != nil {
		t.Fatalf("write mock mcp config: %v", err)
	}

	// git init so Claude detects a project root
	gitInit := exec.Command("git", "init")
	gitInit.Dir = workDir
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init in workdir: %v\n%s", err, out)
	}

	// === DIAGNOSTICS: log workdir state before execution ===
	logWorkdirContents(t, workDir)

	vars := buildVarMap(ctx, workDir)
	resolvedPrompt := substituteVars(reviewerConfig.Prompt, vars)
	vars["$PROMPT"] = resolvedPrompt
	resolvedCommand := substituteVars(reviewerConfig.Command, vars)
	t.Logf("resolved command: %s", resolvedCommand)
	t.Logf("permission mode in command: bypassPermissions=%v",
		strings.Contains(resolvedCommand, "bypassPermissions"))

	// === Execute agent (real claude, timeout 2 min) ===
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	result, err := ExecuteAgent(reviewerConfig, ctx, workDir, 2*time.Minute, logger)
	if err != nil {
		t.Fatalf("agent failed to start: %v", err)
	}

	t.Logf("agent exited: code=%d killed=%v killReason=%s duration=%v",
		result.ExitCode, result.Killed, result.KillReason,
		result.FinishedAt.Sub(result.StartedAt))

	// === DIAGNOSTICS: log agent output ===
	logContent := readLog(result.LogFile)
	t.Logf("agent log:\n%s", logContent)

	// Check for permission-related errors in log
	permissionKeywords := []string{
		"permission",
		"Permission",
		"PERMISSION",
		"not been granted",
		"denied",
		"unauthorized",
		"forbidden",
	}
	for _, kw := range permissionKeywords {
		if strings.Contains(logContent, kw) {
			t.Logf("FOUND permission-related keyword in log: %q", kw)
		}
	}

	// === ASSERTION: expect failure (agent did not call approve/reject) ===
	if !mock.HasCall("approve_pull_request") && !mock.HasCall("reject_pull_request") {
		t.Fatalf("REPRODUCED: agent did not call approve/reject\n"+
			"mock calls: %v\n"+
			"exit code: %d\n"+
			"killed: %v\n"+
			"This is the production failure.",
			mock.GetCalls(), result.ExitCode, result.Killed)
	}

	// If we reach here, the test unexpectedly passed
	t.Logf("UNEXPECTED PASS: agent called approve/reject despite isolated environment")
	t.Logf("mock calls: %v", mock.GetCalls())
}

func TestReviewerLoopE2E(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	// 1. Issue a token (matches production: eventwiring.go issueAgentToken)
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		t.Fatalf("generate token: %v", err)
	}
	token := fmt.Sprintf("%x", tokenBytes)

	// 2. Start mock MCP server with token verification
	mock := NewMockMCPServer(t, token)
	defer mock.Close()

	t.Logf("mock MCP server at %s", mock.URL())

	// 3. Load configs via production code path (built-in from embed)
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatalf("scan agent configs: %v", err)
	}

	reviewerConfig := configs.FindByName("default_claude_reviewer")
	if reviewerConfig == nil {
		t.Fatal("default_claude_reviewer not found in builtin configs")
	}

	if !strings.Contains(reviewerConfig.Command, "bypassPermissions") {
		t.Fatalf("builtin reviewer command missing bypassPermissions: %s", reviewerConfig.Command)
	}

	// 4. Build SpawnContext with token (matches production: eventwiring.go:290)
	ctx := &SpawnContext{
		PRId:         "test/prtest#1",
		PRNumber:     1,
		Namespace:    "test",
		Project:      "prtest",
		SourceBranch: "feat/hello-command",
		TargetBranch: "main",
		OrderFiles:   "/test/prtest/directives/2026-06-28-hello.md",
		ResultFiles:  "/test/prtest/reports/2026-06-28-hello-complete.md",
		Token:        token,
	}

	// 5. PrepareWorkDir via production code path (builtin env from embed)
	workDir, cleanup, err := PrepareWorkDir(reviewerConfig, ctx)
	if err != nil {
		t.Fatalf("prepare workdir: %v", err)
	}
	defer cleanup()

	// 6. Write MCP config with Bearer auth header (matches production: eventwiring.go:302-304)
	if err := writeMockMCPConfig(workDir, mock.URL(), map[string]string{
		"Authorization": "Bearer " + token,
	}); err != nil {
		t.Fatalf("write mock mcp config: %v", err)
	}

	// git init so Claude detects a project root
	gitInit := exec.Command("git", "init")
	gitInit.Dir = workDir
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init in workdir: %v\n%s", err, out)
	}

	t.Logf("workdir: %s", workDir)
	t.Logf("mcp config: %s", filepath.Join(workDir, ".mcp.json"))
	t.Logf("command: %s", reviewerConfig.Command)

	// 7. Execute agent (real claude, timeout 2 min)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	result, err := ExecuteAgent(reviewerConfig, ctx, workDir, 2*time.Minute, logger)
	if err != nil {
		t.Fatalf("agent failed to start: %v", err)
	}

	t.Logf("agent exited: code=%d killed=%v duration=%v",
		result.ExitCode, result.Killed, result.FinishedAt.Sub(result.StartedAt))

	// 8. Check exit
	if result.Killed {
		t.Fatalf("agent was killed (%s)\nlog:\n%s", result.KillReason, readLog(result.LogFile))
	}
	if result.ExitCode != 0 {
		t.Fatalf("agent exit %d\nlog:\n%s", result.ExitCode, readLog(result.LogFile))
	}

	// 9. Check mock received approve or reject
	if !mock.HasCall("approve_pull_request") && !mock.HasCall("reject_pull_request") {
		t.Fatalf("agent did not call approve or reject\ncalls: %v\nlog:\n%s",
			mock.GetCalls(), readLog(result.LogFile))
	}

	// 10. Validate parameters of the call
	for _, call := range mock.GetCalls() {
		if call.Tool != "approve_pull_request" && call.Tool != "reject_pull_request" {
			continue
		}
		t.Logf("agent called %s with params: %v", call.Tool, call.Params)

		if ns, _ := call.Params["namespace"].(string); ns != "test" {
			t.Errorf("namespace = %q, want \"test\"", ns)
		}
		if pn, _ := call.Params["project_name"].(string); pn != "prtest" {
			t.Errorf("project_name = %q, want \"prtest\"", pn)
		}
		switch n := call.Params["number"].(type) {
		case int:
			if n != 1 {
				t.Errorf("number = %d, want 1", n)
			}
		case float64:
			if int(n) != 1 {
				t.Errorf("number = %v, want 1", n)
			}
		default:
			t.Errorf("number type = %T, want int", call.Params["number"])
		}
	}
}
