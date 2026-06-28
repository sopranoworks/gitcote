package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/agent"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authz"
)

func registerAgentTools(mcpServer *mcp.Server, _ *git.Store, agentCfg AgentSpawnConfig, baseDir string, gityardURL string, hs *integrity.Store, logger *slog.Logger) {
	if !agentCfg.IsEnabled() {
		return
	}

	configRoot := agentCfg.EffectiveConfigRoot(baseDir)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "spawn_agent",
		Description: "Manually spawn an agent for testing. Blocks until the agent completes or is killed by timeout. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in spawnAgentInput) (*mcp.CallToolResult, spawnAgentOutput, error) {
		principal, hasPrincipal := auth.PrincipalFrom(ctx)
		if hasPrincipal {
			if err := authz.Authorize(principal.Scope, in.Namespace, "", authz.LevelAdmin); err != nil {
				return nil, spawnAgentOutput{}, fmt.Errorf("admin access required")
			}
		}

		configs, err := agent.ScanAgentConfigs(configRoot)
		if err != nil {
			return nil, spawnAgentOutput{}, fmt.Errorf("scan agent configs: %w", err)
		}

		ac := configs.FindByName(in.AgentName)
		if ac == nil {
			return nil, spawnAgentOutput{}, fmt.Errorf("agent config %q not found", in.AgentName)
		}

		spawnCtx := &agent.SpawnContext{
			PRId:          fmt.Sprintf("%s/%s#%d", in.Namespace, in.ProjectName, in.PRNumber),
			PRNumber:      in.PRNumber,
			Namespace:     in.Namespace,
			Project:       in.ProjectName,
			TargetBranch:  "main",
			Directive:     in.Directive,
			Report:        in.Report,
			GityardMCPURL: gityardURL + "/mcp",
		}

		workDir, cleanup, err := agent.PrepareWorkDir(ac, spawnCtx)
		if err != nil {
			return nil, spawnAgentOutput{}, fmt.Errorf("prepare workdir: %w", err)
		}

		if hs != nil {
			_ = hs.AddAgentWorkdir(integrity.AgentWorkdirRecord{
				Path:      workDir,
				AgentName: ac.DirName,
				Role:      ac.Role,
				Namespace: in.Namespace,
				Project:   in.ProjectName,
				PRNumber:  in.PRNumber,
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
				Status:    "running",
			})
		}

		result, err := agent.ExecuteAgent(ac, spawnCtx, workDir, agentCfg.TimeoutDuration(), logger)
		if err != nil {
			return nil, spawnAgentOutput{}, fmt.Errorf("execute agent: %w", err)
		}

		status := "completed"
		if result.ExitCode != 0 {
			status = "failed"
		}
		if result.Killed {
			status = "killed"
		}

		if result.ExitCode == 0 && !agentCfg.RetainWorkdir {
			cleanup()
			if hs != nil {
				_ = hs.RemoveAgentWorkdir(workDir)
			}
		} else if hs != nil {
			_ = hs.UpdateAgentWorkdir(workDir, status, result.ExitCode)
		}

		return nil, spawnAgentOutput{
			Status:     status,
			ExitCode:   result.ExitCode,
			Killed:     result.Killed,
			KillReason: result.KillReason,
			LogFile:    result.LogFile,
			Duration:   result.FinishedAt.Sub(result.StartedAt).Round(time.Millisecond).String(),
		}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "list_agents",
		Description: "List available agent configurations.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ listAgentsInput) (*mcp.CallToolResult, listAgentsOutput, error) {
		configs, err := agent.ScanAgentConfigs(configRoot)
		if err != nil {
			return nil, listAgentsOutput{}, fmt.Errorf("scan agent configs: %w", err)
		}

		var agents []agentInfo
		for _, c := range configs {
			agents = append(agents, agentInfo{
				Name:        c.DirName,
				Role:        c.Role,
				DisplayName: c.DisplayName,
				HasEnvDir:   c.EnvDir != "",
			})
		}
		return nil, listAgentsOutput{Agents: agents}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "list_agent_workdirs",
		Description: "List tracked agent working directories with status and expiry. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listAgentWorkdirsInput) (*mcp.CallToolResult, listAgentWorkdirsOutput, error) {
		principal, hasPrincipal := auth.PrincipalFrom(ctx)
		if hasPrincipal {
			if err := authz.Authorize(principal.Scope, "", "", authz.LevelAdmin); err != nil {
				return nil, listAgentWorkdirsOutput{}, fmt.Errorf("admin access required")
			}
		}

		if hs == nil {
			return nil, listAgentWorkdirsOutput{}, nil
		}

		recs, err := hs.ListAgentWorkdirs()
		if err != nil {
			return nil, listAgentWorkdirsOutput{}, fmt.Errorf("list workdirs: %w", err)
		}

		var workdirs []workdirInfo
		for _, r := range recs {
			wi := workdirInfo{
				Path:      r.Path,
				AgentName: r.AgentName,
				Role:      r.Role,
				Namespace: r.Namespace,
				Project:   r.Project,
				PRNumber:  r.PRNumber,
				CreatedAt: r.CreatedAt,
				Status:    r.Status,
				ExitCode:  r.ExitCode,
			}
			if created, perr := time.Parse(time.RFC3339, r.CreatedAt); perr == nil && r.Status != "running" {
				wi.ExpiresAt = created.Add(24 * time.Hour).UTC().Format(time.RFC3339)
			}
			workdirs = append(workdirs, wi)
		}
		return nil, listAgentWorkdirsOutput{Workdirs: workdirs}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "delete_agent_workdir",
		Description: "Delete an agent working directory and its tracking record. Refuses to delete a running agent's workdir. Admin only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteAgentWorkdirInput) (*mcp.CallToolResult, deleteAgentWorkdirOutput, error) {
		principal, hasPrincipal := auth.PrincipalFrom(ctx)
		if hasPrincipal {
			if err := authz.Authorize(principal.Scope, "", "", authz.LevelAdmin); err != nil {
				return nil, deleteAgentWorkdirOutput{}, fmt.Errorf("admin access required")
			}
		}

		if hs == nil {
			return nil, deleteAgentWorkdirOutput{}, fmt.Errorf("store not available")
		}

		rec, err := hs.GetAgentWorkdir(in.Path)
		if err != nil {
			return nil, deleteAgentWorkdirOutput{}, fmt.Errorf("get workdir: %w", err)
		}
		if rec == nil {
			return nil, deleteAgentWorkdirOutput{}, fmt.Errorf("workdir not found: %s", in.Path)
		}
		if rec.Status == "running" {
			return nil, deleteAgentWorkdirOutput{}, fmt.Errorf("cannot delete workdir of a running agent")
		}

		if err := os.RemoveAll(in.Path); err != nil && !os.IsNotExist(err) {
			return nil, deleteAgentWorkdirOutput{}, fmt.Errorf("remove workdir: %w", err)
		}
		_ = hs.RemoveAgentWorkdir(in.Path)

		return nil, deleteAgentWorkdirOutput{Message: "workdir deleted"}, nil
	})
}

type spawnAgentInput struct {
	AgentName   string `json:"agent_name" jsonschema:"required,the agent config directory name"`
	Namespace   string `json:"namespace" jsonschema:"required,the namespace"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name"`
	PRNumber    int    `json:"pr_number,omitempty" jsonschema:"optional PR number"`
	Directive   string `json:"directive,omitempty" jsonschema:"optional directive file path in Shoka"`
	Report      string `json:"report,omitempty" jsonschema:"optional report file path in Shoka"`
}

type spawnAgentOutput struct {
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	Killed     bool   `json:"killed,omitempty"`
	KillReason string `json:"kill_reason,omitempty"`
	LogFile    string `json:"log_file"`
	Duration   string `json:"duration"`
}

type listAgentsInput struct{}

type agentInfo struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
	HasEnvDir   bool   `json:"has_env_dir"`
}

type listAgentsOutput struct {
	Agents []agentInfo `json:"agents"`
}

type listAgentWorkdirsInput struct{}

type workdirInfo struct {
	Path      string `json:"path"`
	AgentName string `json:"agent_name"`
	Role      string `json:"role"`
	Namespace string `json:"namespace"`
	Project   string `json:"project"`
	PRNumber  int    `json:"pr_number"`
	CreatedAt string `json:"created_at"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type listAgentWorkdirsOutput struct {
	Workdirs []workdirInfo `json:"workdirs"`
}

type deleteAgentWorkdirInput struct {
	Path string `json:"path" jsonschema:"required,the workdir path to delete"`
}

type deleteAgentWorkdirOutput struct {
	Message string `json:"message"`
}
