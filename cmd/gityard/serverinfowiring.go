package main

import (
	"encoding/json"

	"github.com/sopranoworks/shoka/pkg/authz"
	"github.com/sopranoworks/shoka/pkg/uiws"
)

const MsgServerNetworkInfo uiws.MessageType = "SERVER_NETWORK_INFO"

var ServerInfoLevels = map[uiws.MessageType]uiws.Op{
	MsgServerNetworkInfo: {Level: authz.LevelRead, Global: true},
}

type networkElement struct {
	Label        string `json:"label"`
	Protocol     string `json:"protocol"`
	ListenAddr   string `json:"listen_address"`
	ExternalURL  string `json:"external_url,omitempty"`
	Status       string `json:"status"`
	Description  string `json:"description,omitempty"`
}

type serverInfoContext struct {
	httpListen      string
	httpExternalURL string
	mcpPlainListen  string
	mcpOAuthListen  string
	mcpOAuthExtURL  string
	sshListen       string
	sshExternalURL  string
	integrityStatus *IntegrityStatus
}

func serverInfoDispatch(c *uiws.Client, ctx *serverInfoContext, msgType uiws.MessageType, payload json.RawMessage) bool {
	if msgType != MsgServerNetworkInfo {
		return false
	}
	handleServerNetworkInfo(c, ctx)
	return true
}

func handleServerNetworkInfo(c *uiws.Client, ctx *serverInfoContext) {
	var elements []networkElement

	elements = append(elements, networkElement{
		Label:       "HTTP",
		Protocol:    "http",
		ListenAddr:  ctx.httpListen,
		ExternalURL: ctx.httpExternalURL,
		Status:      "active",
		Description: "WebUI, Auth, Git Smart HTTP transport",
	})

	if ctx.mcpPlainListen != "" {
		elements = append(elements, networkElement{
			Label:       "MCP (plain)",
			Protocol:    "http",
			ListenAddr:  ctx.mcpPlainListen,
			Status:      "active",
			Description: "MCP Streamable HTTP (internal)",
		})
	}

	if ctx.mcpOAuthListen != "" {
		elements = append(elements, networkElement{
			Label:       "MCP (OAuth)",
			Protocol:    "http",
			ListenAddr:  ctx.mcpOAuthListen,
			ExternalURL: ctx.mcpOAuthExtURL,
			Status:      "active",
			Description: "MCP Streamable HTTP (OAuth-protected)",
		})
	}

	if ctx.sshListen != "" {
		elements = append(elements, networkElement{
			Label:       "SSH",
			Protocol:    "ssh",
			ListenAddr:  ctx.sshListen,
			ExternalURL: ctx.sshExternalURL,
			Status:      "active",
			Description: "Git SSH transport (inbound, public key auth)",
		})
	} else {
		elements = append(elements, networkElement{
			Label:       "SSH",
			Protocol:    "ssh",
			ListenAddr:  "",
			Status:      "disabled",
			Description: "Git SSH transport (not configured)",
		})
	}

	resp := map[string]any{"elements": elements}

	if ctx.integrityStatus != nil {
		lastCheck, reposChecked, mismatchCount, alerts := ctx.integrityStatus.snapshot()
		var lastCheckStr string
		if !lastCheck.IsZero() {
			lastCheckStr = lastCheck.UTC().Format("2006-01-02T15:04:05Z")
		}
		resp["integrity"] = map[string]any{
			"last_check_at":   lastCheckStr,
			"repos_checked":   reposChecked,
			"mismatch_count":  mismatchCount,
			"recent_alerts":   alerts,
		}
	}

	c.SendResponse(MsgServerNetworkInfo, resp)
}
