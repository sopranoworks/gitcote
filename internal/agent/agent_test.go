package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"
)

func TestScanAgentConfigs(t *testing.T) {
	dir := t.TempDir()

	// Create two agent configs
	mkAgent(t, dir, "my_reviewer", "reviewer", "Reviewer", "echo review", "Review $PR_ID")
	mkAgent(t, dir, "my_merger", "merger", "", "echo merge", "Merge $PR_ID")
	// Create a directory without agent.yaml (should be skipped)
	os.MkdirAll(filepath.Join(dir, "no_yaml"), 0o755)

	configs, err := ScanAgentConfigs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}

	r := configs.FindByName("my_reviewer")
	if r == nil {
		t.Fatal("FindByName(my_reviewer) returned nil")
	}
	if r.Role != "reviewer" {
		t.Errorf("role = %q, want reviewer", r.Role)
	}
	if r.DisplayName != "Reviewer" {
		t.Errorf("display_name = %q, want Reviewer", r.DisplayName)
	}

	m := configs.FindByName("my_merger")
	if m == nil {
		t.Fatal("FindByName(my_merger) returned nil")
	}
	if m.DisplayName != "my_merger" {
		t.Errorf("display_name = %q, want my_merger (fallback to dir name)", m.DisplayName)
	}

	reviewers := configs.FindByRole("reviewer")
	if len(reviewers) != 1 {
		t.Errorf("FindByRole(reviewer) = %d, want 1", len(reviewers))
	}
}

func TestScanAgentConfigs_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "bad")
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte("agent:\n  role: reviewer\n"), 0o644)

	_, err := ScanAgentConfigs(dir)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestPrepareWorkDir(t *testing.T) {
	envDir := t.TempDir()
	os.WriteFile(filepath.Join(envDir, "CLAUDE.md"), []byte("URL: $GITYARD_MCP_URL\nNS: $NAMESPACE\n"), 0o644)
	// Binary file
	os.WriteFile(filepath.Join(envDir, "binary.dat"), []byte{0x00, 0x01, 0x02, 0x03}, 0o644)

	config := &AgentConfig{
		DirName: "test_agent",
		Role:    "reviewer",
		Command: "echo",
		Prompt:  "test",
		EnvDir:  envDir,
	}

	ctx := &SpawnContext{
		Namespace:     "myns",
		GityardMCPURL: "http://localhost:8081",
	}

	workDir, cleanup, err := PrepareWorkDir(config, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	// Check text file was substituted
	data, err := os.ReadFile(filepath.Join(workDir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "URL: http://localhost:8081") {
		t.Errorf("substitution failed, got: %s", content)
	}
	if !strings.Contains(content, "NS: myns") {
		t.Errorf("namespace substitution failed, got: %s", content)
	}

	// Check binary file was NOT substituted (copied as-is)
	binData, err := os.ReadFile(filepath.Join(workDir, "binary.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(binData) != 4 || binData[0] != 0x00 {
		t.Errorf("binary file was modified")
	}
}

func TestVariableSubstitution_AllVars(t *testing.T) {
	ctx := &SpawnContext{
		PRId:          "ns/proj#1",
		PRNumber:      1,
		Namespace:     "ns",
		Project:       "proj",
		SourceBranch:  "feature",
		TargetBranch:  "main",
		Directive:     "d.md",
		Report:        "r.md",
		GityardMCPURL: "http://g:8081",
		GityardGitURL: "http://g:8080/ns/proj.git",
		GityardSSHURL: "git@g:ns/proj.git",
		ShokaMCPURL:   "http://s:8081",
		TempCloneDir:  "/tmp/tc",
		ConflictFiles: "a.go,b.go",
		Token:         "tok123",
	}

	vars := buildVarMap(ctx, "/work")
	template := "$PR_ID $PR_NUMBER $NAMESPACE $PROJECT $SOURCE_BRANCH $TARGET_BRANCH $DIRECTIVE $REPORT $GITYARD_MCP_URL $GITYARD_GIT_URL $GITYARD_SSH_URL $SHOKA_MCP_URL $TEMP_CLONE_DIR $CONFLICT_FILES $TOKEN $WORK_DIR"
	result := substituteVars(template, vars)

	expected := "ns/proj#1 1 ns proj feature main d.md r.md http://g:8081 http://g:8080/ns/proj.git git@g:ns/proj.git http://s:8081 /tmp/tc a.go,b.go tok123 /work"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestVariableSubstitution_MissingVarsEmpty(t *testing.T) {
	ctx := &SpawnContext{Namespace: "ns"}
	vars := buildVarMap(ctx, "/work")
	result := substituteVars("NS=$NAMESPACE DIR=$DIRECTIVE", vars)
	if result != "NS=ns DIR=" {
		t.Errorf("got %q", result)
	}
}

func TestExecuteAgent_SimpleCommand(t *testing.T) {
	config := &AgentConfig{
		DirName: "test",
		Role:    "reviewer",
		Command: "echo hello $NAMESPACE",
		Prompt:  "test prompt",
	}
	ctx := &SpawnContext{Namespace: "myns"}
	workDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	result, err := ExecuteAgent(config, ctx, workDir, 10*time.Second, logger)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", result.ExitCode)
	}
	if result.Killed {
		t.Error("should not be killed")
	}
	if result.LogFile == "" {
		t.Error("log file should be set")
	}

	logData, err := os.ReadFile(result.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "hello myns") {
		t.Errorf("log should contain 'hello myns', got: %s", string(logData))
	}
}

func TestExecuteAgent_NonZeroExit(t *testing.T) {
	config := &AgentConfig{
		DirName: "test",
		Role:    "reviewer",
		Command: "exit 42",
		Prompt:  "test",
	}
	ctx := &SpawnContext{}
	workDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	result, err := ExecuteAgent(config, ctx, workDir, 10*time.Second, logger)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 42 {
		t.Errorf("exit code = %d, want 42", result.ExitCode)
	}
}

func TestExecuteAgent_HardTimeout(t *testing.T) {
	config := &AgentConfig{
		DirName: "test",
		Role:    "reviewer",
		Command: "sleep 60",
		Prompt:  "test",
	}
	ctx := &SpawnContext{}
	workDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	start := time.Now()
	result, err := ExecuteAgent(config, ctx, workDir, 1*time.Second, logger)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Killed {
		t.Error("should be killed")
	}
	if result.KillReason != "hard_timeout" {
		t.Errorf("kill_reason = %q, want hard_timeout", result.KillReason)
	}
	if elapsed > 15*time.Second {
		t.Errorf("took too long: %v", elapsed)
	}
}

func TestEnsureDefaultAgents(t *testing.T) {
	dir := t.TempDir()

	if err := EnsureDefaultAgents(dir); err != nil {
		t.Fatal(err)
	}

	// Should have created 6 default agent directories
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("expected 6 default agents, got %d", len(entries))
	}

	expectedNames := []string{
		"default_claude_merger",
		"default_claude_reviewer",
		"default_codex_merger",
		"default_codex_reviewer",
		"default_gemini_merger",
		"default_gemini_reviewer",
	}
	for _, name := range expectedNames {
		yamlPath := filepath.Join(dir, name, "agent.yaml")
		if _, err := os.Stat(yamlPath); err != nil {
			t.Errorf("%s: agent.yaml missing", name)
		}
	}

	// Configs should be scannable
	configs, err := ScanAgentConfigs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 6 {
		t.Errorf("scan found %d configs, want 6", len(configs))
	}
	reviewers := configs.FindByRole("reviewer")
	if len(reviewers) != 3 {
		t.Errorf("found %d reviewers, want 3", len(reviewers))
	}
	mergers := configs.FindByRole("merger")
	if len(mergers) != 3 {
		t.Errorf("found %d mergers, want 3", len(mergers))
	}
}

func TestEnsureDefaultAgents_NoOverwrite(t *testing.T) {
	dir := t.TempDir()

	// Create a custom agent that would conflict
	customDir := filepath.Join(dir, "default_claude_reviewer")
	os.MkdirAll(customDir, 0o755)
	os.WriteFile(filepath.Join(customDir, "agent.yaml"), []byte("agent:\n  role: reviewer\n  command: custom\n  prompt: custom\n"), 0o644)

	if err := EnsureDefaultAgents(dir); err != nil {
		t.Fatal(err)
	}

	// Custom should NOT be overwritten
	data, err := os.ReadFile(filepath.Join(customDir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "custom") {
		t.Error("custom agent.yaml was overwritten")
	}
}

func mkAgent(t *testing.T, root, name, role, displayName, command, prompt string) {
	t.Helper()
	dir := filepath.Join(root, name)
	envDir := filepath.Join(dir, "environment_default")
	os.MkdirAll(envDir, 0o755)

	dn := ""
	if displayName != "" {
		dn = "\n  display_name: " + displayName
	}

	yaml := "agent:\n  role: " + role + dn + "\n  command: '" + command + "'\n  prompt: |\n    " + prompt + "\n"
	os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(yaml), 0o644)
	os.WriteFile(filepath.Join(envDir, "README.md"), []byte("test"), 0o644)
}
