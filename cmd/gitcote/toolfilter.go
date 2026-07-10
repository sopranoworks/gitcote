package main

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authz"
)

// levelInternal is a sentinel above LevelAdmin — tools at this level are never
// advertised to any external connection.
const levelInternal = authz.Level(100)

// toolPermissions maps each MCP tool name to the minimum authorization level
// required for the tool to appear in tools/list and be callable. Tools not
// listed here default to LevelRead.
var toolPermissions = map[string]authz.Level{
	// Read
	"get_server_info":        authz.LevelRead,
	"get_pull_request":       authz.LevelRead,
	"get_pull_request_diff":  authz.LevelRead,
	"get_pull_request_files": authz.LevelRead,
	"list_pull_requests":     authz.LevelRead,
	"list_files":             authz.LevelRead,
	"read_file":              authz.LevelRead,
	"list_branches":          authz.LevelRead,
	"get_log":                authz.LevelRead,
	"get_commit":             authz.LevelRead,
	"get_seed_status":        authz.LevelRead,

	// Write
	"approve_pull_request": authz.LevelWrite,
	"reject_pull_request":  authz.LevelWrite,
	"create_pull_request":  authz.LevelWrite,

	// Admin
	"list_projects":        authz.LevelAdmin,
	"push_to_seed":         authz.LevelAdmin,
	"pull_from_seed":       authz.LevelAdmin,
	"retry_pr_agent":       authz.LevelAdmin,
	"dismiss_pr_interrupt": authz.LevelAdmin,
	"retry_seed_sync":      authz.LevelAdmin,
	"dismiss_seed_sync":    authz.LevelAdmin,

	// Internal — never advertised
	"issue_git_token": levelInternal,
}

// toolRequiredLevel returns the minimum level a connection must have for the
// named tool to be advertised and callable. Unknown tools default to LevelRead.
func toolRequiredLevel(name string) authz.Level {
	if lvl, ok := toolPermissions[name]; ok {
		return lvl
	}
	return authz.LevelRead
}

// toolFilterMiddleware returns MCP receiving middleware that filters tools/list
// responses so only tools the connection's principal can use are advertised.
// Defense-in-depth: tool call handlers still enforce authorization independently.
func toolFilterMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || method != "tools/list" {
				return result, err
			}
			tlr, ok := result.(*mcp.ListToolsResult)
			if !ok || tlr == nil {
				return result, err
			}

			principal, hasPrincipal := auth.PrincipalFrom(ctx)
			if !hasPrincipal {
				return result, nil
			}

			grants := authz.ParseScope(principal.Scope)
			maxLevel := authz.EffectiveLevel(grants, "", "")

			filtered := make([]*mcp.Tool, 0, len(tlr.Tools))
			for _, t := range tlr.Tools {
				if toolRequiredLevel(t.Name) <= maxLevel {
					filtered = append(filtered, t)
				}
			}
			tlr.Tools = filtered
			return tlr, nil
		}
	}
}

// injectPrincipal returns HTTP middleware that attaches principal p to the
// request context when no principal is already present.
func injectPrincipal(p auth.Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.PrincipalFrom(r.Context()); !ok {
				r = r.WithContext(auth.WithPrincipal(r.Context(), p))
			}
			next.ServeHTTP(w, r)
		})
	}
}
