package git

import (
	"net/http"

	"github.com/sopranoworks/shoka/pkg/auth"
)

// BasicAuthMiddleware extracts an OAuth/PAT token from the HTTP Basic Auth
// password field (username is ignored — the standard pattern for git + PAT used
// by GitHub/GitLab/GitBucket) and attaches the resolved principal to the request
// context. When no credentials are provided the request proceeds without a
// principal (the downstream handler decides whether to allow it). When credentials
// are provided but invalid a 401 is returned immediately.
//
// validateToken is the same closure used by the MCP OAuth transport; nil means
// auth is disabled (all requests proceed without a principal).
func BasicAuthMiddleware(validateToken func(string) (auth.Principal, auth.RejectReason, bool)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if validateToken == nil {
				next.ServeHTTP(w, r)
				return
			}
			_, password, ok := r.BasicAuth()
			if !ok || password == "" {
				// Auth is required but no credentials provided — challenge.
				w.Header().Set("WWW-Authenticate", `Basic realm="GitYard"`)
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
			principal, _, valid := validateToken(password)
			if !valid {
				w.Header().Set("WWW-Authenticate", `Basic realm="GitYard"`)
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			ctx := auth.WithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
