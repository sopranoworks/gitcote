package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpConfig struct {
	MCPServers map[string]struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers,omitempty"`
	} `json:"mcpServers"`
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}

func main() {
	ns := os.Getenv("NAMESPACE")
	proj := os.Getenv("PROJECT")
	prNumStr := os.Getenv("PR_NUMBER")
	prNum, _ := strconv.Atoi(prNumStr)

	if ns == "" || proj == "" || prNum == 0 {
		fmt.Fprintf(os.Stderr, "mock-rejector: NAMESPACE=%q PROJECT=%q PR_NUMBER=%q — all required\n", ns, proj, prNumStr)
		os.Exit(1)
	}

	data, err := os.ReadFile(".mcp.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-rejector: read .mcp.json: %v\n", err)
		os.Exit(1)
	}

	var cfg mcpConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "mock-rejector: parse .mcp.json: %v\n", err)
		os.Exit(1)
	}

	server, ok := cfg.MCPServers["gitcote"]
	if !ok {
		fmt.Fprintf(os.Stderr, "mock-rejector: no 'gitcote' server in .mcp.json\n")
		os.Exit(1)
	}

	fmt.Printf("mock-rejector: connecting to %s for %s/%s#%d\n", server.URL, ns, proj, prNum)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	if len(server.Headers) > 0 {
		httpClient.Transport = &headerTransport{
			base:    http.DefaultTransport,
			headers: server.Headers,
		}
	}

	client := mcp.NewClient(
		&mcp.Implementation{Name: "mock-rejector", Version: "1.0"},
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	transport := &mcp.StreamableClientTransport{
		Endpoint:             server.URL,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-rejector: MCP connect: %v\n", err)
		os.Exit(1)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_pull_request",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(prNum),
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-rejector: get_pull_request: %v\n", err)
		os.Exit(1)
	}
	if result.IsError {
		fmt.Fprintf(os.Stderr, "mock-rejector: get_pull_request returned error\n")
		os.Exit(1)
	}
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			fmt.Printf("mock-rejector: PR info: %s\n", tc.Text)
		}
	}

	rejectResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "reject_pull_request",
		Arguments: map[string]any{
			"namespace":    ns,
			"project_name": proj,
			"number":       float64(prNum),
			"reason":       "Code does not meet quality standards",
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-rejector: reject_pull_request: %v\n", err)
		os.Exit(1)
	}
	if rejectResult.IsError {
		fmt.Fprintf(os.Stderr, "mock-rejector: reject_pull_request returned error\n")
		os.Exit(1)
	}

	fmt.Printf("mock-rejector: PR %s/%s#%d rejected successfully\n", ns, proj, prNum)
}
