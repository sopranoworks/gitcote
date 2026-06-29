package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/gityard/internal/pr"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
)

func TestCorrectPath_PushToPRToReviewToApprove(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	ns, proj := "default", "testproject"
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// --- Step 1: Create repo, commit on main, commit on feature branch ---
	if err := gitStore.CreateRepo(ns, proj); err != nil {
		t.Fatal(err)
	}
	repo, err := gitStore.OpenRepo(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(baseDir, ns, proj)

	writeTestFile(t, projDir, "README.md", "# Test Project\n")
	mainHash := commitAllFiles(t, repo, projDir, "main", "initial commit")

	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main")))

	writeTestFile(t, projDir, "feature.go", "package main\n\nfunc hello() {}\n")
	featHash := commitAllFiles(t, repo, projDir, "feat/hello", "add feature")

	repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main")))

	t.Logf("main=%s feat/hello=%s", mainHash.String()[:8], featHash.String()[:8])

	// --- Step 1: git push → PR creation via push options ---
	integrityStore, err := integrity.Open(filepath.Join(baseDir, "repo_heads.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer integrityStore.Close()

	headStore = integrityStore

	handlePostReceive(gitStore, logger, ns, proj,
		auth.Principal{Name: "coder", Email: "coder@test.com"},
		[]string{"pull_request.create", "pull_request.target=main"},
		nil,
	)

	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}

	prs, err := prStore.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	thePR := prs[0]
	if thePR.SourceBranch != "feat/hello" {
		t.Fatalf("PR source = %q, want feat/hello", thePR.SourceBranch)
	}
	if thePR.TargetBranch != "main" {
		t.Fatalf("PR target = %q, want main", thePR.TargetBranch)
	}
	if thePR.State != pr.StateOpen {
		t.Fatalf("PR state = %q, want open", thePR.State)
	}
	t.Logf("Step 1 PASS: PR #%d created (source=%s target=%s state=%s)",
		thePR.Number, thePR.SourceBranch, thePR.TargetBranch, thePR.State)

	// --- Step 2: event hook resolution ---
	enabled := true
	globalSettings := &integrity.PREventSettings{
		OnCreated: &integrity.EventAction{
			AgentEnabled: &enabled,
			AgentName:    "default_claude_reviewer",
		},
	}
	if err := integrityStore.SetGlobalPREventSettings(globalSettings); err != nil {
		t.Fatal(err)
	}

	resolved, err := integrityStore.ResolvePREventSettings(ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	action := integrity.ResolveEventAction(resolved.OnCreated, globalSettings.OnCreated)
	if !action.AgentEnabled {
		t.Fatal("Step 2 FAIL: agent not enabled in resolved settings")
	}
	if action.AgentName != "default_claude_reviewer" {
		t.Fatalf("Step 2 FAIL: agent name = %q", action.AgentName)
	}
	t.Logf("Step 2 PASS: event hook resolves to agent=%s enabled=%v", action.AgentName, action.AgentEnabled)

	// --- Step 3: per-spawn OAuth token issuance ---
	oauthDB := filepath.Join(baseDir, "oauth.db")
	oauthSt, err := oauthstore.Open(oauthDB)
	if err != nil {
		t.Fatal(err)
	}
	defer oauthSt.Close()

	evtCtx := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityStore,
		oauthStore:  oauthSt,
		agentCfg:    AgentSpawnConfig{},
		logger:      logger,
	}

	token, err := issueAgentToken(evtCtx, ns, proj, int(thePR.Number), thePR.SourceBranch, "default_claude_reviewer", "reviewer")
	if err != nil {
		t.Fatalf("Step 3 FAIL: issue token: %v", err)
	}
	if token == "" {
		t.Fatal("Step 3 FAIL: empty token")
	}
	t.Logf("Step 3 PASS: token issued (len=%d)", len(token))

	// --- Step 4: token authenticates on MCP ---
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gityard-test", Version: "0.0.0-test"},
		nil,
	)

	registerPRTools(mcpServer, gitStore, &seedContext{gitStore: gitStore}, evtCtx)

	authenticator := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			if tok == "" {
				return auth.Principal{}, auth.ReasonMissingBearer, false
			}
			rec, lerr := oauthSt.Lookup(tok, time.Now())
			if lerr != nil {
				return auth.Principal{}, auth.ReasonInvalidToken, false
			}
			scope := rec.Scope
			if scope == "" {
				scope = "*"
			}
			return auth.Principal{
				Name:     rec.Principal.Name,
				Email:    rec.Principal.Email,
				Scope:    scope,
				ClientID: rec.ClientID,
			}, "", true
		},
	})

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		nil,
	)
	handler := authenticator.Middleware(mcpHandler)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Verify: no token → 401
	resp := mcpPost(t, ts.URL, "", "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":   map[string]any{},
		"clientInfo":     map[string]any{"name": "test", "version": "1.0"},
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Step 4: no-token should be 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify: agent token → passes auth
	resp = mcpPost(t, ts.URL, token, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":   map[string]any{},
		"clientInfo":     map[string]any{"name": "test", "version": "1.0"},
	})
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Fatalf("Step 4 FAIL: agent token rejected (%d)", resp.StatusCode)
	}
	resp.Body.Close()
	t.Logf("Step 4 PASS: agent token authenticates (status=%d)", resp.StatusCode)

	// --- Step 5+6: Full MCP client → approve_pull_request → PR state change ---
	client := mcp.NewClient(
		&mcp.Implementation{Name: "test-client", Version: "1.0"},
		nil,
	)
	ctx := context.Background()

	transport := &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &bearerTransport{token: token}},
		DisableStandaloneSSE: true,
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("Step 6: connect: %v", err)
	}
	t.Logf("Step 6: connected to MCP server")

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "approve_pull_request",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(thePR.Number),
		},
	})
	if err != nil {
		t.Fatalf("Step 6 FAIL: approve_pull_request via MCP: %v", err)
	}
	if result.IsError {
		t.Fatalf("Step 6 FAIL: approve_pull_request returned error")
	}
	t.Logf("Step 6: approve_pull_request succeeded")

	final, err := prStore.Get(thePR.Number)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != pr.StateApproved {
		t.Fatalf("Step 6 FAIL: PR state after approve = %q, want approved", final.State)
	}
	t.Logf("Step 6 PASS: Full MCP path works — PR #%d approved via token-authenticated MCP call", final.Number)
}

type bearerTransport struct {
	token string
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(req)
}

func mcpPost(t *testing.T, baseURL, token, method string, params map[string]any) *http.Response {
	t.Helper()
	body := buildMCPRequest(method, params)
	req, _ := http.NewRequest("POST", baseURL+"/mcp", body)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
