package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gitcote/pkg/agent"
	"github.com/sopranoworks/gitcote/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	nsProject  string
	agentName  string
	keepWorkdir bool
	configPath string
	branch     string
	verbose    bool
	showVersion bool
	prompt     string
}

func parseArgs(args []string) (*options, error) {
	fs := flag.NewFlagSet("dovefeeder", flag.ContinueOnError)
	var opts options

	fs.StringVar(&opts.nsProject, "n", "", "namespace/project (required)")
	fs.StringVar(&opts.nsProject, "namespace-project", "", "namespace/project (required)")
	fs.StringVar(&opts.agentName, "a", "", "agent template name (required)")
	fs.StringVar(&opts.agentName, "agent", "", "agent template name (required)")
	fs.BoolVar(&opts.keepWorkdir, "k", false, "keep working directory after completion")
	fs.BoolVar(&opts.keepWorkdir, "keep-workdir", false, "keep working directory after completion")
	fs.StringVar(&opts.configPath, "c", "", "config file path")
	fs.StringVar(&opts.configPath, "config", "", "config file path")
	fs.StringVar(&opts.branch, "b", "", "branch to work on")
	fs.StringVar(&opts.branch, "branch", "", "branch to work on")
	fs.BoolVar(&opts.verbose, "v", false, "verbose output")
	fs.BoolVar(&opts.verbose, "verbose", false, "verbose output")
	fs.BoolVar(&opts.showVersion, "version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if opts.showVersion {
		return &opts, nil
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		return nil, fmt.Errorf("prompt is required as positional argument")
	}
	opts.prompt = strings.Join(remaining, " ")

	if opts.nsProject == "" {
		return nil, fmt.Errorf("-n/--namespace-project is required")
	}
	if opts.agentName == "" {
		return nil, fmt.Errorf("-a/--agent is required")
	}

	parts := strings.SplitN(opts.nsProject, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("namespace-project must be in format namespace/project, got %q", opts.nsProject)
	}

	return &opts, nil
}

func resolvePrompt(prompt string) (string, error) {
	if strings.HasPrefix(prompt, "@") {
		path := prompt[1:]
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read prompt file %s: %w", path, err)
		}
		return string(data), nil
	}
	return prompt, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	if opts.showVersion {
		fmt.Fprintf(stdout, "dovefeeder %s\n", version.Version)
		return 0
	}

	prompt, err := resolvePrompt(opts.prompt)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	logLevel := slog.LevelWarn
	if opts.verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: logLevel}))

	cfg, err := LoadConfig(opts.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	parts := strings.SplitN(opts.nsProject, "/", 2)
	namespace, project := parts[0], parts[1]

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	code, err := execute(ctx, cfg, namespace, project, opts.agentName, opts.branch, prompt, opts.keepWorkdir, opts.verbose, logger, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
	}
	return code
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

func execute(
	ctx context.Context,
	cfg *Config,
	namespace, project, agentName, branch, prompt string,
	keepWorkdir, verbose bool,
	logger *slog.Logger,
	stdout, stderr io.Writer,
) (int, error) {
	// Load agent template
	configs, err := agent.ScanAgentConfigs("")
	if err != nil {
		return 1, fmt.Errorf("scan agent configs: %w", err)
	}
	agentCfg := configs.FindByName(agentName)
	if agentCfg == nil {
		var names []string
		for _, c := range configs {
			names = append(names, c.DirName)
		}
		return 1, fmt.Errorf("agent %q not found; available: %s", agentName, strings.Join(names, ", "))
	}

	// Connect to GitCote MCP
	fmt.Fprintf(stdout, "Connecting to GitCote MCP at %s...\n", cfg.GitCote.MCPURL)
	transport := &mcp.StreamableClientTransport{
		Endpoint: cfg.GitCote.MCPURL,
		HTTPClient: &http.Client{
			Transport: &bearerTransport{
				token: cfg.GitCote.OAuthToken,
				base:  http.DefaultTransport,
			},
		},
		DisableStandaloneSSE: true,
	}

	mcpClient := mcp.NewClient(
		&mcp.Implementation{Name: "dovefeeder", Version: version.Version},
		&mcp.ClientOptions{Logger: logger},
	)
	session, err := mcpClient.Connect(ctx, transport, nil)
	if err != nil {
		return 1, fmt.Errorf("MCP connection to %s failed: %w", cfg.GitCote.MCPURL, err)
	}
	defer session.Close()

	// Verify connection
	fmt.Fprintf(stdout, "Verifying connection...\n")
	_, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_projects",
		Arguments: map[string]any{"namespace": namespace},
	})
	if err != nil {
		return 1, fmt.Errorf("connection verification failed (list_projects): %w", err)
	}

	// Issue git token
	fmt.Fprintf(stdout, "Issuing git token for %s/%s...\n", namespace, project)
	tokenArgs := map[string]any{
		"namespace":    namespace,
		"project_name": project,
		"scope":        "rw",
		"ttl":          "2h",
	}
	if branch != "" {
		tokenArgs["allowed_branches"] = []string{branch}
	}

	tokenResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "issue_git_token",
		Arguments: tokenArgs,
	})
	if err != nil {
		return 1, fmt.Errorf("token issuance failed (do you have admin access on namespace %q?): %w", namespace, err)
	}
	if tokenResult.IsError {
		return 1, fmt.Errorf("token issuance failed: %s", extractText(tokenResult))
	}

	tokenData, err := extractStructured(tokenResult)
	if err != nil {
		return 1, fmt.Errorf("parse token response: %w", err)
	}

	token, _ := tokenData["token"].(string)
	cloneURL, _ := tokenData["clone_url"].(string)
	if token == "" || cloneURL == "" {
		return 1, fmt.Errorf("token response missing token or clone_url")
	}

	maskedToken := token
	if len(maskedToken) > 8 {
		maskedToken = maskedToken[:8] + "..."
	}
	fmt.Fprintf(stdout, "Token issued: %s (expires: %s)\n", maskedToken, tokenData["expires_at"])

	// Prepare working directory
	suffix := randomSuffix()
	workDir := filepath.Join(os.TempDir(), fmt.Sprintf("dovefeeder-%s-%s-%s", namespace, project, suffix))
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return 1, fmt.Errorf("create workdir: %w", err)
	}

	cleanupWorkdir := func() {
		if !keepWorkdir {
			os.RemoveAll(workDir)
		}
	}

	// Clone the repository
	authedCloneURL := injectTokenInURL(cloneURL, token)
	fmt.Fprintf(stdout, "Cloning repository to %s...\n", workDir)
	cloneCmd := exec.CommandContext(ctx, "git", "clone", authedCloneURL, ".")
	cloneCmd.Dir = workDir
	cloneCmd.Stdout = io.Discard
	cloneCmd.Stderr = stderr
	if err := cloneCmd.Run(); err != nil {
		cleanupWorkdir()
		return 1, fmt.Errorf("clone failed (url: %s): %w", cloneURL, err)
	}

	// Branch handling
	if branch != "" {
		checkoutCmd := exec.CommandContext(ctx, "git", "checkout", "-B", branch)
		checkoutCmd.Dir = workDir
		checkoutCmd.Stdout = io.Discard
		checkoutCmd.Stderr = stderr
		if err := checkoutCmd.Run(); err != nil {
			cleanupWorkdir()
			return 1, fmt.Errorf("checkout branch %q: %w", branch, err)
		}
	}

	// Set up agent environment
	fmt.Fprintf(stdout, "Setting up agent environment (%s)...\n", agentName)
	spawnCtx := &agent.SpawnContext{
		Namespace: namespace,
		Project:   project,
		GitURL:    authedCloneURL,
		Token:     token,
	}

	envWorkDir, cleanup, err := agent.PrepareWorkDir(agentCfg, spawnCtx)
	if err != nil {
		cleanupWorkdir()
		return 1, fmt.Errorf("prepare agent environment: %w", err)
	}
	defer cleanup()

	// Copy environment files into the clone directory
	if err := copyDirContents(envWorkDir, workDir); err != nil {
		cleanupWorkdir()
		return 1, fmt.Errorf("copy agent environment: %w", err)
	}

	// Write .mcp.json for the agent
	mcpURL := cfg.GitCote.MCPURL
	err = agent.WriteMCPConfig(workDir, map[string]agent.MCPServerEntry{
		"gitcote": {
			Type: "streamable-http",
			URL:  mcpURL,
			Headers: map[string]string{
				"Authorization": "Bearer " + cfg.GitCote.OAuthToken,
			},
		},
	})
	if err != nil {
		cleanupWorkdir()
		return 1, fmt.Errorf("write MCP config: %w", err)
	}

	// Resolve the agent command with variable substitution
	vars := map[string]string{
		"$NAMESPACE": namespace,
		"$PROJECT":   project,
		"$GIT_URL":   authedCloneURL,
		"$TOKEN":     token,
		"$WORK_DIR":  workDir,
		"$PROMPT":    prompt,
	}
	resolvedCommand := substituteVars(agentCfg.Command, vars)

	// Spawn agent
	fmt.Fprintf(stdout, "Spawning agent: %s\n", agentCfg.DisplayName)
	fmt.Fprintf(stdout, "Working directory: %s\n", workDir)
	fmt.Fprintf(stdout, "---\n")

	agentCmd := exec.CommandContext(ctx, "sh", "-c", resolvedCommand)
	agentCmd.Dir = workDir
	agentCmd.Stdout = stdout
	agentCmd.Stderr = stderr
	agentCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	envVars := os.Environ()
	for k, v := range vars {
		envVars = append(envVars, strings.TrimPrefix(k, "$")+"="+v)
	}
	agentCmd.Env = envVars

	startedAt := time.Now()

	if err := agentCmd.Start(); err != nil {
		cleanupWorkdir()
		return 1, fmt.Errorf("start agent (%s not found in PATH?): %w", agentName, err)
	}

	waitErr := agentCmd.Wait()
	elapsed := time.Since(startedAt)

	fmt.Fprintf(stdout, "---\n")

	if waitErr != nil {
		exitCode := 1
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "Agent exited with code %d after %s\n", exitCode, elapsed.Round(time.Second))
		fmt.Fprintf(stderr, "Working directory preserved: %s\n", workDir)
		return exitCode, nil
	}

	fmt.Fprintf(stdout, "Agent completed successfully in %s\n", elapsed.Round(time.Second))
	if keepWorkdir {
		fmt.Fprintf(stdout, "Working directory: %s\n", workDir)
	} else {
		cleanupWorkdir()
		fmt.Fprintf(stdout, "Working directory cleaned up\n")
	}
	return 0, nil
}

func extractText(result *mcp.CallToolResult) string {
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func extractStructured(result *mcp.CallToolResult) (map[string]any, error) {
	if result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		return m, nil
	}
	text := extractText(result)
	if text == "" {
		return nil, fmt.Errorf("no content in result")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func injectTokenInURL(cloneURL, token string) string {
	if strings.HasPrefix(cloneURL, "https://") {
		return "https://" + token + "@" + cloneURL[len("https://"):]
	}
	if strings.HasPrefix(cloneURL, "http://") {
		return "http://" + token + "@" + cloneURL[len("http://"):]
	}
	return cloneURL
}

func substituteVars(text string, vars map[string]string) string {
	for k, v := range vars {
		text = strings.ReplaceAll(text, k, v)
	}
	return text
}

func copyDirContents(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

func randomSuffix() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
