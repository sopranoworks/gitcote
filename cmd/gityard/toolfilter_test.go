package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/gityard/internal/vault"
	"github.com/sopranoworks/shoka/pkg/auth"
)

// setupToolFilterTest creates one MCP server with all tools registered and the
// toolFilterMiddleware enabled. It returns two test servers:
//   - plainTS: no auth, read-only principal injected (simulates plain MCP transport)
//   - oauthTS: OAuth-style token validation, principal from token
func setupToolFilterTest(t *testing.T) (plainTS, oauthTS *httptest.Server) {
	t.Helper()

	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "test", "prtest"

	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	repo, err := gitStore.OpenRepo(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(baseDir, ns, proj)

	writeTestFile(t, projDir, "README.md", "# Test\n")
	commitAllFiles(t, repo, projDir, "main", "initial commit")
	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main")))

	writeTestFile(t, projDir, "feature.go", "package main\n")
	commitAllFiles(t, repo, projDir, "feat/hello", "add feature")
	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main")))

	intStore, err := integrity.Open(filepath.Join(baseDir, "heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { intStore.Close() })
	headStore = intStore

	handlePostReceive(gitStore, testLogger(), ns, proj,
		auth.Principal{Name: "coder", Email: "coder@test.com"},
		[]string{"pull_request.create", "pull_request.target=main"}, nil)

	v, err := vault.Open(filepath.Join(baseDir, "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })

	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gityard-test", Version: "0.0.0-test"}, nil)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "get_server_info", Description: "Server info",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ serverInfoInput) (*mcp.CallToolResult, serverInfoOutput, error) {
		return nil, serverInfoOutput{Name: "test", Version: "0.0.0"}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "list_projects", Description: "List projects",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listProjectsInput) (*mcp.CallToolResult, listProjectsOutput, error) {
		projects, lerr := gitStore.ListProjects(in.Namespace)
		if lerr != nil {
			return nil, listProjectsOutput{}, lerr
		}
		return nil, listProjectsOutput{Projects: projects}, nil
	})

	registerPRTools(mcpServer, gitStore, &seedContext{gitStore: gitStore},
		&eventContext{gitStore: gitStore, integrityHS: intStore})
	registerRepoTools(mcpServer, gitStore)
	registerSeedTools(mcpServer, gitStore, v, "", nil)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "issue_git_token", Description: "Issue git token (internal)",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
		return nil, struct{}{}, nil
	})

	mcpServer.AddReceivingMiddleware(toolFilterMiddleware())

	// Plain MCP server: no auth, read-only principal injected.
	plainPrincipal := auth.Principal{Name: "plain-user", Email: "plain@test.com", Scope: "*:r"}
	plainMCPHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer }, nil)
	plainTS = httptest.NewServer(injectPrincipal(plainPrincipal)(plainMCPHandler))
	t.Cleanup(plainTS.Close)

	// OAuth MCP server: validates tokens, principal from token.
	tokens := map[string]string{
		"rw":    "*:rw",
		"admin": "*:admin",
	}
	oauthAuth := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			scope, ok := tokens[tok]
			if !ok {
				return auth.Principal{}, auth.ReasonInvalidToken, false
			}
			return auth.Principal{Name: "test-agent", Scope: scope}, "", true
		},
	})
	oauthMCPHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer }, nil)
	oauthTS = httptest.NewServer(oauthAuth.Middleware(oauthMCPHandler))
	t.Cleanup(oauthTS.Close)

	return plainTS, oauthTS
}

func connectMCPTo(t *testing.T, url string, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
	var httpClient *http.Client
	if token != "" {
		httpClient = &http.Client{Transport: &bearerTransport{token: token}}
	} else {
		httpClient = http.DefaultClient
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:             url + "/mcp",
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return session
}

func listToolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := make([]string, len(result.Tools))
	for i, tool := range result.Tools {
		names[i] = tool.Name
	}
	return names
}

func hasToolName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// Test 1: Plain MCP sees only read tools.
func TestToolFilter_PlainMCPReadOnly(t *testing.T) {
	plainTS, _ := setupToolFilterTest(t)

	session := connectMCPTo(t, plainTS.URL, "")
	tools := listToolNames(t, session)

	readTools := []string{
		"get_server_info", "get_pull_request", "get_pull_request_diff",
		"get_pull_request_files", "list_pull_requests", "list_files",
		"read_file", "list_branches", "get_log", "get_commit", "get_seed_status",
	}
	for _, name := range readTools {
		if !hasToolName(tools, name) {
			t.Errorf("read tool %q should be visible to plain MCP, got tools: %v", name, tools)
		}
	}

	writeTools := []string{"approve_pull_request", "reject_pull_request", "create_pull_request"}
	for _, name := range writeTools {
		if hasToolName(tools, name) {
			t.Errorf("write tool %q should NOT be visible to plain MCP", name)
		}
	}

	adminTools := []string{"list_projects", "push_to_seed", "pull_from_seed", "retry_pr_agent", "dismiss_pr_interrupt"}
	for _, name := range adminTools {
		if hasToolName(tools, name) {
			t.Errorf("admin tool %q should NOT be visible to plain MCP", name)
		}
	}

	if hasToolName(tools, "issue_git_token") {
		t.Error("internal tool issue_git_token should NOT be visible to plain MCP")
	}
}

// Test 2: OAuth rw token sees read + write tools, not admin.
func TestToolFilter_RWToken(t *testing.T) {
	_, oauthTS := setupToolFilterTest(t)

	session := connectMCPTo(t, oauthTS.URL, "rw")
	tools := listToolNames(t, session)

	if !hasToolName(tools, "get_pull_request") {
		t.Error("rw token should see read tool get_pull_request")
	}
	if !hasToolName(tools, "approve_pull_request") {
		t.Error("rw token should see write tool approve_pull_request")
	}
	if !hasToolName(tools, "reject_pull_request") {
		t.Error("rw token should see write tool reject_pull_request")
	}

	adminTools := []string{"list_projects", "push_to_seed", "pull_from_seed"}
	for _, name := range adminTools {
		if hasToolName(tools, name) {
			t.Errorf("admin tool %q should NOT be visible to rw token", name)
		}
	}

	if hasToolName(tools, "issue_git_token") {
		t.Error("internal tool issue_git_token should NOT be visible to rw token")
	}
}

// Test 3: Admin token sees all non-internal tools.
func TestToolFilter_AdminToken(t *testing.T) {
	_, oauthTS := setupToolFilterTest(t)

	session := connectMCPTo(t, oauthTS.URL, "admin")
	tools := listToolNames(t, session)

	expectedTools := []string{
		"get_server_info", "get_pull_request", "get_pull_request_diff",
		"get_pull_request_files", "list_pull_requests", "list_files",
		"read_file", "list_branches", "get_log", "get_commit", "get_seed_status",
		"approve_pull_request", "reject_pull_request", "create_pull_request",
		"list_projects", "push_to_seed", "pull_from_seed",
		"retry_pr_agent", "dismiss_pr_interrupt",
	}
	for _, name := range expectedTools {
		if !hasToolName(tools, name) {
			t.Errorf("admin token should see tool %q, got tools: %v", name, tools)
		}
	}

	if hasToolName(tools, "issue_git_token") {
		t.Error("internal tool issue_git_token should NOT be visible even to admin")
	}
}

// Test 4: Plain MCP cannot call write tools (defense in depth).
func TestToolFilter_PlainMCPCannotCallWriteTool(t *testing.T) {
	plainTS, _ := setupToolFilterTest(t)

	session := connectMCPTo(t, plainTS.URL, "")
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "approve_pull_request",
		Arguments: map[string]any{
			"namespace": "test", "project_name": "prtest", "number": float64(1),
		},
	})
	if err == nil && result != nil && !result.IsError {
		t.Fatal("plain MCP should not be able to call approve_pull_request, but it succeeded")
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	} else if result != nil && result.IsError {
		for _, c := range result.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				errMsg = tc.Text
			}
		}
	}
	if !strings.Contains(errMsg, "access denied") {
		t.Fatalf("expected 'access denied' error, got: %q", errMsg)
	}
}

// Test 5: Plain MCP can call read tools successfully.
func TestToolFilter_PlainMCPCanCallReadTool(t *testing.T) {
	plainTS, _ := setupToolFilterTest(t)

	session := connectMCPTo(t, plainTS.URL, "")
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_pull_request",
		Arguments: map[string]any{
			"namespace": "test", "project_name": "prtest", "number": float64(1),
		},
	})
	if err != nil {
		t.Fatalf("plain MCP should be able to call get_pull_request: %v", err)
	}
	if result.IsError {
		msg := ""
		for _, c := range result.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msg = tc.Text
			}
		}
		t.Fatalf("get_pull_request returned error for plain MCP: %s", msg)
	}
}
