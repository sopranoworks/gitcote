package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

func NewMockMCPServer(t *testing.T) *MockMCPServer {
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

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		nil,
	)

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

func writeMockMCPConfig(workDir, mockURL string) error {
	claudeDir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		return err
	}
	config := map[string]any{
		"mcpServers": map[string]any{
			"gityard": map[string]any{
				"type": "url",
				"url":  mockURL,
			},
		},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(claudeDir, "mcp.json"), data, 0o644)
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

func TestReviewerLoopE2E(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}

	// 1. Start mock MCP server
	mock := NewMockMCPServer(t)
	defer mock.Close()

	t.Logf("mock MCP server at %s", mock.URL())

	// 2. Build SpawnContext
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

	// 3. Create workdir with git init and MCP config
	workDir := t.TempDir()

	gitInit := exec.Command("git", "init")
	gitInit.Dir = workDir
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init in workdir: %v\n%s", err, out)
	}

	if err := writeMockMCPConfig(workDir, mock.URL()); err != nil {
		t.Fatalf("write mock mcp config: %v", err)
	}

	// 4. Build reviewer config with --strict-mcp-config to isolate from user-level MCP
	mcpConfigPath := filepath.Join(workDir, ".claude", "mcp.json")
	reviewerConfig := &AgentConfig{
		DirName: "test_claude_reviewer",
		Role:    "reviewer",
		Command: fmt.Sprintf(
			`claude --permission-mode bypassPermissions --strict-mcp-config --mcp-config %s -p "$PROMPT"`,
			mcpConfigPath,
		),
		Prompt: `Review PR $PR_ID ($SOURCE_BRANCH → $TARGET_BRANCH).

Use available MCP tools to read the PR diff and files.
If order files are provided ($ORDER_FILES), read them for context.
If result files are provided ($RESULT_FILES), read them for context.

Call approve_pull_request or reject_pull_request when done.`,
	}

	t.Logf("workdir: %s", workDir)
	t.Logf("mcp config: %s", mcpConfigPath)

	// 5. Execute agent (real claude, timeout 2 min)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	result, err := ExecuteAgent(reviewerConfig, ctx, workDir, 2*time.Minute, logger)
	if err != nil {
		t.Fatalf("agent failed to start: %v", err)
	}

	t.Logf("agent exited: code=%d killed=%v duration=%v",
		result.ExitCode, result.Killed, result.FinishedAt.Sub(result.StartedAt))

	// 7. Check exit
	if result.Killed {
		t.Fatalf("agent was killed (%s)\nlog:\n%s", result.KillReason, readLog(result.LogFile))
	}
	if result.ExitCode != 0 {
		t.Fatalf("agent exit %d\nlog:\n%s", result.ExitCode, readLog(result.LogFile))
	}

	// 8. Check mock received approve or reject
	if !mock.HasCall("approve_pull_request") && !mock.HasCall("reject_pull_request") {
		t.Fatalf("agent did not call approve or reject\ncalls: %v\nlog:\n%s",
			mock.GetCalls(), readLog(result.LogFile))
	}

	// 9. Validate parameters of the call
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
