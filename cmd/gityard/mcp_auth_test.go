package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/vault"
	"github.com/sopranoworks/shoka/pkg/auth"
)

func TestMCPAuthE2E(t *testing.T) {
	baseDir := t.TempDir()
	gitStore := git.NewStore(baseDir)
	v, err := vault.Open(filepath.Join(baseDir, "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gityard-test", Version: "0.0.0-test"},
		nil,
	)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_server_info",
		Description: "Test tool",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ serverInfoInput) (*mcp.CallToolResult, serverInfoOutput, error) {
		return nil, serverInfoOutput{Name: "test", Version: "0.0.0"}, nil
	})
	registerRepoTools(mcpServer, gitStore)
	registerSeedTools(mcpServer, gitStore, v)

	// Create the MCP HTTP handler with authentication.
	validToken := "test-token-12345"
	authenticator := auth.New(auth.Config{
		Enabled: true,
		Tokens:  []string{validToken},
	})

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		nil,
	)
	handler := authenticator.Middleware(mcpHandler)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Test 1: Request without token → rejected (401).
	t.Run("no_token_rejected", func(t *testing.T) {
		body := buildMCPRequest("initialize", map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":   map[string]interface{}{},
			"clientInfo":     map[string]interface{}{"name": "test", "version": "1.0"},
		})
		resp, err := http.Post(ts.URL+"/mcp", "application/json", body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("no-token: status = %d, want 401", resp.StatusCode)
		}
	})

	// Test 2: Request with invalid token → rejected (401).
	t.Run("bad_token_rejected", func(t *testing.T) {
		body := buildMCPRequest("initialize", map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":   map[string]interface{}{},
			"clientInfo":     map[string]interface{}{"name": "test", "version": "1.0"},
		})
		req, _ := http.NewRequest("POST", ts.URL+"/mcp", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer bad-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("bad-token: status = %d, want 401", resp.StatusCode)
		}
	})

	// Test 3: Request with valid token → passes auth (not 401/403).
	// The MCP protocol layer may return various codes depending on session state,
	// but the auth layer should not block the request.
	t.Run("valid_token_passes_auth", func(t *testing.T) {
		body := buildMCPRequest("initialize", map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":   map[string]interface{}{},
			"clientInfo":     map[string]interface{}{"name": "test", "version": "1.0"},
		})
		req, _ := http.NewRequest("POST", ts.URL+"/mcp", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+validToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		// Auth passes: must NOT be 401 or 403.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			t.Errorf("valid-token: status = %d, should not be auth-rejected", resp.StatusCode)
		}
	})
}

func buildMCPRequest(method string, params interface{}) *bytes.Buffer {
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	data, _ := json.Marshal(msg)
	return bytes.NewBuffer(data)
}
