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

	// Create two user agent configs
	mkAgent(t, dir, "my_reviewer", "reviewer", "Reviewer", "echo review", "Review $PR_ID")
	mkAgent(t, dir, "my_merger", "merger", "", "echo merge", "Merge $PR_ID")
	// Create a directory without agent.yaml (should be skipped)
	os.MkdirAll(filepath.Join(dir, "no_yaml"), 0o755)

	configs, err := ScanAgentConfigs(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Should include 8 built-in + 2 user configs
	builtins, _ := builtinNames()
	expectedCount := len(builtins) + 2
	if len(configs) != expectedCount {
		t.Fatalf("expected %d configs (%d builtin + 2 user), got %d", expectedCount, len(builtins), len(configs))
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
	if r.IsBuiltin {
		t.Error("user config should not be marked as builtin")
	}

	m := configs.FindByName("my_merger")
	if m == nil {
		t.Fatal("FindByName(my_merger) returned nil")
	}
	if m.DisplayName != "my_merger" {
		t.Errorf("display_name = %q, want my_merger (fallback to dir name)", m.DisplayName)
	}

	reviewers := configs.FindByRole("reviewer")
	if len(reviewers) < 1 {
		t.Errorf("FindByRole(reviewer) = %d, want >= 1", len(reviewers))
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

func TestScanAgentConfigs_BuiltinDefaults(t *testing.T) {
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatal(err)
	}

	if len(configs) != 8 {
		t.Fatalf("expected 8 builtin configs, got %d", len(configs))
	}

	expectedNames := []string{
		"default_claude_coder",
		"default_claude_merger",
		"default_claude_reviewer",
		"default_codex_merger",
		"default_codex_reviewer",
		"default_gemini_coder",
		"default_gemini_merger",
		"default_gemini_reviewer",
	}
	for _, name := range expectedNames {
		c := configs.FindByName(name)
		if c == nil {
			t.Errorf("builtin %s not found", name)
			continue
		}
		if !c.IsBuiltin {
			t.Errorf("%s should be marked as builtin", name)
		}
	}

	reviewers := configs.FindByRole("reviewer")
	if len(reviewers) != 3 {
		t.Errorf("found %d reviewers, want 3", len(reviewers))
	}
	mergers := configs.FindByRole("merger")
	if len(mergers) != 3 {
		t.Errorf("found %d mergers, want 3", len(mergers))
	}
	coders := configs.FindByRole("coder")
	if len(coders) != 2 {
		t.Errorf("found %d coders, want 2", len(coders))
	}
}

func TestScanAgentConfigs_UserOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()

	mkAgent(t, dir, "default_claude_reviewer", "reviewer", "Custom Reviewer", "custom-cmd", "custom prompt")

	configs, err := ScanAgentConfigs(dir)
	if err != nil {
		t.Fatal(err)
	}

	r := configs.FindByName("default_claude_reviewer")
	if r == nil {
		t.Fatal("default_claude_reviewer not found")
	}
	if r.Command != "custom-cmd" {
		t.Errorf("expected user override command, got %q", r.Command)
	}
	if r.IsBuiltin {
		t.Error("overridden config should not be marked as builtin")
	}
	if r.DisplayName != "Custom Reviewer" {
		t.Errorf("display_name = %q, want Custom Reviewer", r.DisplayName)
	}
}

func TestScanAgentConfigs_EmptyConfigRoot(t *testing.T) {
	dir := t.TempDir()
	configs, err := ScanAgentConfigs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 8 {
		t.Fatalf("expected 8 builtin configs with empty configRoot dir, got %d", len(configs))
	}
}

func TestScanAgentConfigs_NonexistentConfigRoot(t *testing.T) {
	configs, err := ScanAgentConfigs("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 8 {
		t.Fatalf("expected 8 builtin configs with nonexistent configRoot, got %d", len(configs))
	}
}

func TestPrepareWorkDir(t *testing.T) {
	envDir := t.TempDir()
	os.WriteFile(filepath.Join(envDir, "CLAUDE.md"), []byte("NS: $NAMESPACE\nPR: $PR_ID\n"), 0o644)
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
		Namespace: "myns",
		PRId:      "myns/proj#1",
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
	if !strings.Contains(content, "NS: myns") {
		t.Errorf("namespace substitution failed, got: %s", content)
	}
	if !strings.Contains(content, "PR: myns/proj#1") {
		t.Errorf("PR_ID substitution failed, got: %s", content)
	}

	// Check binary file was NOT substituted (copied as-is)
	binData, err := os.ReadFile(filepath.Join(workDir, "binary.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(binData) != 4 || binData[0] != 0x00 {
		t.Errorf("binary file was modified")
	}

	// Verify NO .mcp.json generated
	if _, err := os.Stat(filepath.Join(workDir, ".mcp.json")); !os.IsNotExist(err) {
		t.Error(".mcp.json should NOT be generated in workdir")
	}
}

func TestPrepareWorkDir_Builtin(t *testing.T) {
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatal(err)
	}

	reviewer := configs.FindByName("default_claude_reviewer")
	if reviewer == nil {
		t.Fatal("default_claude_reviewer not found")
	}
	if !reviewer.IsBuiltin {
		t.Fatal("expected builtin config")
	}

	ctx := &SpawnContext{
		PRId:      "ns/proj#1",
		Namespace: "ns",
		Project:   "proj",
	}

	workDir, cleanup, err := PrepareWorkDir(reviewer, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	claudeMD, err := os.ReadFile(filepath.Join(workDir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(claudeMD) == 0 {
		t.Error("CLAUDE.md should not be empty")
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
		TempCloneDir:  "/tmp/tc",
		ConflictFiles: "a.go,b.go",
		OrderFiles:    "/shoka/dev/directives/d.md",
		ResultFiles:   "/shoka/dev/reports/r.md,/shoka/dev/reports/r2.md",
		Token:         "tok123",
	}

	vars := buildVarMap(ctx, "/work")
	template := "$PR_ID $PR_NUMBER $NAMESPACE $PROJECT $SOURCE_BRANCH $TARGET_BRANCH $DIRECTIVE $REPORT $TEMP_CLONE_DIR $CONFLICT_FILES $ORDER_FILES $RESULT_FILES $TOKEN $WORK_DIR"
	result := substituteVars(template, vars)

	expected := "ns/proj#1 1 ns proj feature main d.md r.md /tmp/tc a.go,b.go /shoka/dev/directives/d.md /shoka/dev/reports/r.md,/shoka/dev/reports/r2.md tok123 /work"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestVariableSubstitution_NoMCPURLVars(t *testing.T) {
	ctx := &SpawnContext{Namespace: "ns"}
	vars := buildVarMap(ctx, "/work")

	for _, key := range []string{"$GITCOTE_MCP_URL", "$GITCOTE_GIT_URL", "$GITCOTE_SSH_URL", "$SHOKA_MCP_URL"} {
		if _, ok := vars[key]; ok {
			t.Errorf("%s should NOT be in variable map", key)
		}
	}
}

func TestSpawnContextOrderResultFiles(t *testing.T) {
	ctx := &SpawnContext{
		PRId:        "ns/proj#1",
		PRNumber:    1,
		Namespace:   "ns",
		Project:     "proj",
		OrderFiles:  "/shoka/dev/directives/task.md,/shoka/dev/specs/api.md",
		ResultFiles: "/shoka/dev/reports/complete.md",
	}

	vars := buildVarMap(ctx, "/work")

	if vars["$ORDER_FILES"] != "/shoka/dev/directives/task.md,/shoka/dev/specs/api.md" {
		t.Errorf("ORDER_FILES = %q, want comma-separated paths", vars["$ORDER_FILES"])
	}
	if vars["$RESULT_FILES"] != "/shoka/dev/reports/complete.md" {
		t.Errorf("RESULT_FILES = %q, want single path", vars["$RESULT_FILES"])
	}

	prompt := "Review PR. Orders: $ORDER_FILES Results: $RESULT_FILES"
	resolved := substituteVars(prompt, vars)
	if !strings.Contains(resolved, "/shoka/dev/directives/task.md") {
		t.Error("ORDER_FILES not substituted into prompt")
	}
	if !strings.Contains(resolved, "/shoka/dev/reports/complete.md") {
		t.Error("RESULT_FILES not substituted into prompt")
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

func TestPrepareWorkDir_NoMCPJson(t *testing.T) {
	envDir := t.TempDir()
	os.WriteFile(filepath.Join(envDir, "CLAUDE.md"), []byte("review PR $PR_ID"), 0o644)

	config := &AgentConfig{
		DirName: "test_agent",
		Role:    "reviewer",
		Command: "echo",
		Prompt:  "test",
		EnvDir:  envDir,
	}
	ctx := &SpawnContext{
		PRId:      "ns/proj#1",
		Namespace: "ns",
		Project:   "proj",
	}

	workDir, cleanup, err := PrepareWorkDir(config, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(workDir, ".mcp.json")); !os.IsNotExist(err) {
		t.Error(".mcp.json must NOT exist in workdir")
	}
}

func TestPrepareWorkDir_CleanCLAUDEmd(t *testing.T) {
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatal(err)
	}

	reviewer := configs.FindByName("default_claude_reviewer")
	if reviewer == nil {
		t.Fatal("default_claude_reviewer not found")
	}

	ctx := &SpawnContext{
		PRId:         "ns/proj#1",
		PRNumber:     1,
		Namespace:    "ns",
		Project:      "proj",
		SourceBranch: "feature",
		TargetBranch: "main",
		OrderFiles:   "directives/task.md",
		ResultFiles:  "reports/complete.md",
	}

	workDir, cleanup, err := PrepareWorkDir(reviewer, ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	claudeMD, err := os.ReadFile(filepath.Join(workDir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(claudeMD)

	for _, banned := range []string{"$GITCOTE_MCP_URL", "$SHOKA_MCP_URL", "GITCOTE_MCP_URL", "SHOKA_MCP_URL", "shoka", "Shoka"} {
		if strings.Contains(content, banned) {
			t.Errorf("CLAUDE.md contains banned reference %q:\n%s", banned, content)
		}
	}

	if _, err := os.Stat(filepath.Join(workDir, ".mcp.json")); !os.IsNotExist(err) {
		t.Error(".mcp.json must NOT exist in workdir")
	}
}

func TestResolvedPrompt_OrderResultFiles(t *testing.T) {
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatal(err)
	}

	reviewer := configs.FindByName("default_claude_reviewer")
	if reviewer == nil {
		t.Fatal("default_claude_reviewer not found")
	}

	ctx := &SpawnContext{
		PRId:         "ns/proj#1",
		PRNumber:     1,
		Namespace:    "ns",
		Project:      "proj",
		SourceBranch: "feature",
		TargetBranch: "main",
		OrderFiles:   "directives/task.md,specs/api.md",
		ResultFiles:  "reports/complete.md",
	}

	vars := buildVarMap(ctx, "/work")
	resolved := substituteVars(reviewer.Prompt, vars)

	if !strings.Contains(resolved, "ns/proj#1") {
		t.Error("$PR_ID not substituted")
	}
	if !strings.Contains(resolved, "directives/task.md,specs/api.md") {
		t.Error("$ORDER_FILES not substituted")
	}
	if !strings.Contains(resolved, "reports/complete.md") {
		t.Error("$RESULT_FILES not substituted")
	}
	if strings.Contains(resolved, "$GITCOTE_MCP_URL") {
		t.Error("prompt still contains $GITCOTE_MCP_URL")
	}
	if strings.Contains(resolved, "$SHOKA_MCP_URL") {
		t.Error("prompt still contains $SHOKA_MCP_URL")
	}
}

func TestResolvedCommand_BypassPermissions(t *testing.T) {
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatal(err)
	}

	reviewer := configs.FindByName("default_claude_reviewer")
	if reviewer == nil {
		t.Fatal("default_claude_reviewer not found")
	}

	ctx := &SpawnContext{
		PRId:         "ns/proj#1",
		PRNumber:     1,
		Namespace:    "ns",
		Project:      "proj",
		SourceBranch: "feature",
		TargetBranch: "main",
	}

	vars := buildVarMap(ctx, "/work")
	resolvedPrompt := substituteVars(reviewer.Prompt, vars)
	vars["$PROMPT"] = resolvedPrompt
	resolvedCommand := substituteVars(reviewer.Command, vars)

	if !strings.Contains(resolvedCommand, "bypassPermissions") {
		t.Errorf("command should contain bypassPermissions, got: %s", resolvedCommand)
	}
}

func TestDefaultAgentPrompts_NoMCPURLReferences(t *testing.T) {
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatal(err)
	}

	banned := []string{"$GITCOTE_MCP_URL", "$GITCOTE_GIT_URL", "$GITCOTE_SSH_URL", "$SHOKA_MCP_URL", "Shoka"}

	for _, c := range configs {
		if !c.IsBuiltin {
			continue
		}
		for _, b := range banned {
			if strings.Contains(c.Prompt, b) {
				t.Errorf("agent %s prompt contains banned reference %q", c.DirName, b)
			}
		}

		if c.HasEnvDir() {
			ctx := &SpawnContext{Namespace: "test"}
			workDir, cleanup, err := PrepareWorkDir(&c, ctx)
			if err != nil {
				t.Errorf("prepare workdir for %s: %v", c.DirName, err)
				continue
			}
			entries, err := os.ReadDir(workDir)
			if err != nil {
				cleanup()
				t.Errorf("read workdir %s: %v", c.DirName, err)
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				data, err := os.ReadFile(filepath.Join(workDir, e.Name()))
				if err != nil {
					continue
				}
				content := string(data)
				for _, b := range banned {
					if strings.Contains(content, b) {
						t.Errorf("agent %s file %s contains banned reference %q", c.DirName, e.Name(), b)
					}
				}
			}
			cleanup()
		}
	}
}

func TestBuiltinHasEnvDir(t *testing.T) {
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatal(err)
	}

	claudeReviewer := configs.FindByName("default_claude_reviewer")
	if claudeReviewer == nil {
		t.Fatal("default_claude_reviewer not found")
	}
	if !claudeReviewer.HasEnvDir() {
		t.Error("default_claude_reviewer should have env dir")
	}

	codexReviewer := configs.FindByName("default_codex_reviewer")
	if codexReviewer == nil {
		t.Fatal("default_codex_reviewer not found")
	}
	if codexReviewer.HasEnvDir() {
		t.Error("default_codex_reviewer should not have env dir")
	}
}

func TestPrepareWorkDir_PrepareShHook(t *testing.T) {
	t.Run("succeeds", func(t *testing.T) {
		envDir := t.TempDir()
		os.WriteFile(filepath.Join(envDir, "prepare.sh"), []byte("echo \"prepared\" > $WORK_DIR/prepared.txt\n"), 0o755)

		config := &AgentConfig{
			DirName: "test_agent",
			Role:    "reviewer",
			Command: "echo",
			Prompt:  "test",
			EnvDir:  envDir,
		}
		ctx := &SpawnContext{Namespace: "ns", PRId: "ns/proj#1"}

		workDir, cleanup, err := PrepareWorkDir(config, ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()

		data, err := os.ReadFile(filepath.Join(workDir, "prepared.txt"))
		if err != nil {
			t.Fatal("marker file not created by prepare.sh")
		}
		if strings.TrimSpace(string(data)) != "prepared" {
			t.Errorf("marker content = %q, want \"prepared\"", strings.TrimSpace(string(data)))
		}
	})

	t.Run("fails", func(t *testing.T) {
		envDir := t.TempDir()
		os.WriteFile(filepath.Join(envDir, "prepare.sh"), []byte("exit 1\n"), 0o755)

		config := &AgentConfig{
			DirName: "test_agent",
			Role:    "reviewer",
			Command: "echo",
			Prompt:  "test",
			EnvDir:  envDir,
		}
		ctx := &SpawnContext{Namespace: "ns"}

		workDir, _, err := PrepareWorkDir(config, ctx)
		if err == nil {
			t.Fatal("expected error from failing prepare.sh")
		}
		if !strings.Contains(err.Error(), "prepare.sh failed") {
			t.Errorf("error = %v, want containing \"prepare.sh failed\"", err)
		}
		if _, serr := os.Stat(workDir); !os.IsNotExist(serr) {
			t.Error("workdir should be cleaned up after prepare.sh failure")
		}
	})

	t.Run("not_present", func(t *testing.T) {
		envDir := t.TempDir()
		os.WriteFile(filepath.Join(envDir, "README.md"), []byte("no prepare.sh here"), 0o644)

		config := &AgentConfig{
			DirName: "test_agent",
			Role:    "reviewer",
			Command: "echo",
			Prompt:  "test",
			EnvDir:  envDir,
		}
		ctx := &SpawnContext{Namespace: "ns"}

		workDir, cleanup, err := PrepareWorkDir(config, ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()

		if _, serr := os.Stat(workDir); os.IsNotExist(serr) {
			t.Error("workdir should exist when prepare.sh is absent")
		}
	})

	t.Run("env_vars_passed", func(t *testing.T) {
		envDir := t.TempDir()
		os.WriteFile(filepath.Join(envDir, "prepare.sh"), []byte("echo \"${PR_ID}|${NAMESPACE}\" > $WORK_DIR/env_check.txt\n"), 0o755)

		config := &AgentConfig{
			DirName: "test_agent",
			Role:    "reviewer",
			Command: "echo",
			Prompt:  "test",
			EnvDir:  envDir,
		}
		ctx := &SpawnContext{
			PRId:      "testns/testproj#42",
			Namespace: "testns",
		}

		workDir, cleanup, err := PrepareWorkDir(config, ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()

		data, err := os.ReadFile(filepath.Join(workDir, "env_check.txt"))
		if err != nil {
			t.Fatal("env_check.txt not created")
		}
		got := strings.TrimSpace(string(data))
		want := "testns/testproj#42|testns"
		if got != want {
			t.Errorf("env vars = %q, want %q", got, want)
		}
	})
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

func TestWriteMCPConfig_BothFormats(t *testing.T) {
	workDir := t.TempDir()

	err := WriteMCPConfig(workDir, map[string]MCPServerEntry{
		"gitcote": {Type: "http", URL: "http://localhost:9999/mcp", Headers: map[string]string{"Authorization": "Bearer tok"}},
	})
	if err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}

	claudeConfig, err := os.ReadFile(filepath.Join(workDir, ".mcp.json"))
	if err != nil {
		t.Fatal(".mcp.json not created")
	}

	geminiConfig, err := os.ReadFile(filepath.Join(workDir, ".gemini", "settings.json"))
	if err != nil {
		t.Fatal(".gemini/settings.json not created")
	}

	if string(claudeConfig) != string(geminiConfig) {
		t.Error(".mcp.json and .gemini/settings.json content differ")
	}

	if !strings.Contains(string(claudeConfig), `"gitcote"`) {
		t.Error("config missing gitcote server entry")
	}
	if !strings.Contains(string(claudeConfig), `"Bearer tok"`) {
		t.Error("config missing auth header")
	}
}

func TestBuiltinGeminiConfigs(t *testing.T) {
	configs, err := ScanAgentConfigs("")
	if err != nil {
		t.Fatal(err)
	}

	reviewer := configs.FindByName("default_gemini_reviewer")
	if reviewer == nil {
		t.Fatal("default_gemini_reviewer not found")
	}
	if reviewer.Role != "reviewer" {
		t.Errorf("reviewer role = %q, want reviewer", reviewer.Role)
	}
	if !strings.Contains(reviewer.Command, "--yolo") {
		t.Errorf("reviewer command missing --yolo: %s", reviewer.Command)
	}
	if !strings.Contains(reviewer.Command, "--skip-trust") {
		t.Errorf("reviewer command missing --skip-trust: %s", reviewer.Command)
	}

	merger := configs.FindByName("default_gemini_merger")
	if merger == nil {
		t.Fatal("default_gemini_merger not found")
	}
	if merger.Role != "merger" {
		t.Errorf("merger role = %q, want merger", merger.Role)
	}
	if !strings.Contains(merger.Command, "--yolo") {
		t.Errorf("merger command missing --yolo: %s", merger.Command)
	}
	if !strings.Contains(merger.Prompt, "$GIT_URL") {
		t.Errorf("merger prompt missing $GIT_URL: %s", merger.Prompt)
	}
}
