package agent

import (
	"log/slog"
	"time"

	pkg "github.com/sopranoworks/gitcote/pkg/agent"
)

type AgentConfig = pkg.AgentConfig
type AgentConfigs = pkg.AgentConfigs
type SpawnContext = pkg.SpawnContext
type SpawnResult = pkg.SpawnResult
type MCPServerEntry = pkg.MCPServerEntry

func ScanAgentConfigs(configRoot string) (AgentConfigs, error) {
	return pkg.ScanAgentConfigs(configRoot)
}

func PrepareWorkDir(config *AgentConfig, ctx *SpawnContext) (string, func(), error) {
	return pkg.PrepareWorkDir(config, ctx)
}

func WriteMCPConfig(workDir string, servers map[string]MCPServerEntry) error {
	return pkg.WriteMCPConfig(workDir, servers)
}

func ExecuteAgent(config *AgentConfig, ctx *SpawnContext, workDir string, hardTimeout time.Duration, logger *slog.Logger) (*SpawnResult, error) {
	return pkg.ExecuteAgent(config, ctx, workDir, hardTimeout, logger)
}
