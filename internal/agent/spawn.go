package agent

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type SpawnContext struct {
	PRId         string
	PRNumber     int
	Namespace    string
	Project      string
	SourceBranch string
	TargetBranch string
	Directive    string
	Report       string
	TempCloneDir string
	ConflictFiles string
	OrderFiles   string
	ResultFiles  string
	ReviewFiles  string
	Token        string
}

type SpawnResult struct {
	ExitCode   int
	StartedAt  time.Time
	FinishedAt time.Time
	Killed     bool
	KillReason string
	LogFile    string
}

func PrepareWorkDir(config *AgentConfig, ctx *SpawnContext) (workDir string, cleanup func(), err error) {
	suffix, _ := randomHex(8)
	workDir = filepath.Join(os.TempDir(), fmt.Sprintf("gityard-agent-%s-%s", config.Role, suffix))
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create workdir: %w", err)
	}

	cleanup = func() { os.RemoveAll(workDir) }

	vars := buildVarMap(ctx, workDir)

	if config.IsBuiltin {
		if hasBuiltinEnvDefault(config.DirName) {
			if err := copyBuiltinEnvDefault(config.DirName, workDir, vars); err != nil {
				cleanup()
				return "", nil, fmt.Errorf("copy builtin environment_default: %w", err)
			}
		}
	} else if config.EnvDir != "" {
		if err := copyDirWithSubstitution(config.EnvDir, workDir, vars); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("copy environment_default: %w", err)
		}
	}

	prepareScript := filepath.Join(workDir, "prepare.sh")
	if _, serr := os.Stat(prepareScript); serr == nil {
		cmd := exec.Command("sh", prepareScript)
		cmd.Dir = workDir
		envVars := os.Environ()
		for k, v := range vars {
			envVars = append(envVars, strings.TrimPrefix(k, "$")+"="+v)
		}
		cmd.Env = envVars
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		if rerr := cmd.Run(); rerr != nil {
			cleanup()
			return "", nil, fmt.Errorf("prepare.sh failed: %w\noutput: %s", rerr, output.String())
		}
	}

	return workDir, cleanup, nil
}

func WriteMCPConfig(workDir string, servers map[string]MCPServerEntry) error {
	config := map[string]any{"mcpServers": servers}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workDir, ".mcp.json"), data, 0o644)
}

type MCPServerEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

func ExecuteAgent(
	config *AgentConfig,
	ctx *SpawnContext,
	workDir string,
	hardTimeout time.Duration,
	logger *slog.Logger,
) (*SpawnResult, error) {
	vars := buildVarMap(ctx, workDir)

	resolvedPrompt := substituteVars(config.Prompt, vars)
	vars["$PROMPT"] = resolvedPrompt
	resolvedCommand := substituteVars(config.Command, vars)

	suffix, _ := randomHex(8)
	logFile := filepath.Join(os.TempDir(), fmt.Sprintf("gityard-agent-%s-%s.log", config.Role, suffix))

	lf, err := os.Create(logFile)
	if err != nil {
		return nil, fmt.Errorf("create log file: %w", err)
	}
	defer lf.Close()

	logWriter := io.MultiWriter(lf, &slogWriter{logger: logger, role: config.Role})

	cmd := exec.Command("sh", "-c", resolvedCommand)
	cmd.Dir = workDir
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	envVars := os.Environ()
	for k, v := range vars {
		envName := strings.TrimPrefix(k, "$")
		envVars = append(envVars, envName+"="+v)
	}
	cmd.Env = envVars

	result := &SpawnResult{
		StartedAt: time.Now(),
		LogFile:   logFile,
	}

	if err := cmd.Start(); err != nil {
		result.FinishedAt = time.Now()
		result.ExitCode = -1
		return result, fmt.Errorf("start command: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	hardTimer := time.NewTimer(hardTimeout)
	defer hardTimer.Stop()

	select {
	case err := <-done:
		result.FinishedAt = time.Now()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			} else {
				result.ExitCode = -1
			}
		}
		return result, nil

	case <-hardTimer.C:
		result.Killed = true
		result.KillReason = "hard_timeout"
		logger.Warn("agent hard timeout, sending SIGTERM",
			"role", config.Role, "pid", cmd.Process.Pid)

		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			logger.Warn("agent did not exit after SIGTERM, sending SIGKILL",
				"role", config.Role, "pid", cmd.Process.Pid)
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}

		result.FinishedAt = time.Now()
		result.ExitCode = -1
		return result, nil
	}
}

func buildVarMap(ctx *SpawnContext, workDir string) map[string]string {
	m := map[string]string{
		"$PR_ID":           ctx.PRId,
		"$PR_NUMBER":       fmt.Sprintf("%d", ctx.PRNumber),
		"$NAMESPACE":       ctx.Namespace,
		"$PROJECT":         ctx.Project,
		"$SOURCE_BRANCH":   ctx.SourceBranch,
		"$TARGET_BRANCH":   ctx.TargetBranch,
		"$DIRECTIVE":      ctx.Directive,
		"$REPORT":         ctx.Report,
		"$TEMP_CLONE_DIR": ctx.TempCloneDir,
		"$CONFLICT_FILES":  ctx.ConflictFiles,
		"$ORDER_FILES":     ctx.OrderFiles,
		"$RESULT_FILES":    ctx.ResultFiles,
		"$REVIEW_FILES":    ctx.ReviewFiles,
		"$TOKEN":           ctx.Token,
		"$WORK_DIR":        workDir,
	}
	return m
}

func substituteVars(text string, vars map[string]string) string {
	for k, v := range vars {
		text = strings.ReplaceAll(text, k, v)
	}
	return text
}

func copyDirWithSubstitution(src, dst string, vars map[string]string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if isBinary(data) {
			return os.WriteFile(target, data, info.Mode())
		}

		substituted := substituteVars(string(data), vars)
		return os.WriteFile(target, []byte(substituted), info.Mode())
	})
}

func isBinary(data []byte) bool {
	check := data
	if len(check) > 512 {
		check = check[:512]
	}
	return bytes.ContainsRune(check, 0)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

type slogWriter struct {
	logger *slog.Logger
	role   string
}

func (w *slogWriter) Write(p []byte) (int, error) {
	w.logger.Info("agent output", "role", w.role, "output", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
