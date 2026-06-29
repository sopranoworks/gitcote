package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/gityard/internal/pr"
	"github.com/sopranoworks/shoka/pkg/auth"
)

func TestScopeEnforcement(t *testing.T) {
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
	defer intStore.Close()
	headStore = intStore

	handlePostReceive(gitStore, testLogger(), ns, proj,
		auth.Principal{Name: "coder", Email: "coder@test.com"},
		[]string{"pull_request.create", "pull_request.target=main"}, nil)

	prStore, err := getPRStore(baseDir, ns, proj)
	if err != nil {
		t.Fatal(err)
	}
	prs, _ := prStore.List("")
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	prNum := prs[0].Number

	tokens := map[string]string{
		"read":      "namespace:test/prtest:r",
		"readwrite": "namespace:test/prtest:rw",
		"wrong_ns":  "namespace:other/prtest:r",
		"wrong_proj": "namespace:test/other:r",
		"git_only":  "git/namespace:test/prtest:rw",
	}

	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gityard-test", Version: "0.0.0-test"}, nil)
	registerPRTools(mcpServer, gitStore, &seedContext{gitStore: gitStore},
		&eventContext{gitStore: gitStore, integrityHS: intStore})

	authenticator := auth.New(auth.Config{
		ValidateToken: func(tok string) (auth.Principal, auth.RejectReason, bool) {
			scope, ok := tokens[tok]
			if !ok {
				return auth.Principal{}, auth.ReasonInvalidToken, false
			}
			return auth.Principal{Name: "test-agent", Scope: scope}, "", true
		},
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer }, nil)
	ts := httptest.NewServer(authenticator.Middleware(mcpHandler))
	defer ts.Close()

	connectMCP := func(t *testing.T, token string) *mcp.ClientSession {
		t.Helper()
		client := mcp.NewClient(
			&mcp.Implementation{Name: "test-client", Version: "1.0"}, nil)
		transport := &mcp.StreamableClientTransport{
			Endpoint:             ts.URL + "/mcp",
			HTTPClient:           &http.Client{Transport: &bearerTransport{token: token}},
			DisableStandaloneSSE: true,
		}
		session, err := client.Connect(context.Background(), transport, nil)
		if err != nil {
			t.Fatalf("connect with token %q: %v", token, err)
		}
		return session
	}

	callTool := func(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (bool, string) {
		t.Helper()
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      tool,
			Arguments: args,
		})
		if err != nil {
			return false, err.Error()
		}
		if result.IsError {
			msg := ""
			for _, c := range result.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					msg = tc.Text
				}
			}
			return false, msg
		}
		return true, ""
	}

	readArgs := map[string]any{
		"namespace": ns, "project_name": proj, "number": float64(prNum),
	}

	t.Run("read_scoped_token", func(t *testing.T) {
		session := connectMCP(t, "read")

		for _, tool := range []string{"get_pull_request", "get_pull_request_diff", "get_pull_request_files"} {
			t.Run(tool+"_allowed", func(t *testing.T) {
				ok, msg := callTool(t, session, tool, readArgs)
				if !ok {
					t.Fatalf("%s should succeed with r scope, got error: %s", tool, msg)
				}
			})
		}

		for _, tool := range []string{"approve_pull_request", "reject_pull_request"} {
			t.Run(tool+"_denied", func(t *testing.T) {
				ok, msg := callTool(t, session, tool, readArgs)
				if ok {
					t.Fatalf("%s should fail with r scope, but succeeded", tool)
				}
				if !strings.Contains(msg, "access denied") {
					t.Fatalf("%s: expected 'access denied', got %q", tool, msg)
				}
			})
		}
	})

	t.Run("readwrite_scoped_token", func(t *testing.T) {
		session := connectMCP(t, "readwrite")

		for _, tool := range []string{"get_pull_request", "get_pull_request_diff", "get_pull_request_files"} {
			t.Run(tool+"_allowed", func(t *testing.T) {
				ok, msg := callTool(t, session, tool, readArgs)
				if !ok {
					t.Fatalf("%s should succeed with rw scope, got error: %s", tool, msg)
				}
			})
		}

		t.Run("approve_allowed", func(t *testing.T) {
			ok, msg := callTool(t, session, "approve_pull_request", readArgs)
			if !ok {
				t.Fatalf("approve should succeed with rw scope, got error: %s", msg)
			}
			p, _ := prStore.Get(prNum)
			if p.State != pr.StateApproved {
				t.Fatalf("PR state = %q, want approved", p.State)
			}
		})
	})

	t.Run("namespace_scoping", func(t *testing.T) {
		session := connectMCP(t, "wrong_ns")

		t.Run("get_pull_request_denied", func(t *testing.T) {
			ok, msg := callTool(t, session, "get_pull_request", readArgs)
			if ok {
				t.Fatal("should fail with wrong namespace token, but succeeded")
			}
			if !strings.Contains(msg, "access denied") {
				t.Fatalf("expected 'access denied', got %q", msg)
			}
		})
	})

	t.Run("project_scoping", func(t *testing.T) {
		session := connectMCP(t, "wrong_proj")

		t.Run("get_pull_request_denied", func(t *testing.T) {
			ok, msg := callTool(t, session, "get_pull_request", readArgs)
			if ok {
				t.Fatal("should fail with wrong project token, but succeeded")
			}
			if !strings.Contains(msg, "access denied") {
				t.Fatalf("expected 'access denied', got %q", msg)
			}
		})
	})

	t.Run("git_zone_denied_mcp", func(t *testing.T) {
		session := connectMCP(t, "git_only")

		for _, tool := range []string{"get_pull_request", "get_pull_request_diff", "approve_pull_request"} {
			t.Run(tool+"_denied", func(t *testing.T) {
				ok, msg := callTool(t, session, tool, readArgs)
				if ok {
					t.Fatalf("%s should fail with git/-only token, but succeeded", tool)
				}
				if !strings.Contains(msg, "access denied") {
					t.Fatalf("%s: expected 'access denied', got %q", tool, msg)
				}
			})
		}
	})
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}
