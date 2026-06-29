package git

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/backend"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage"
	"github.com/go-git/go-git/v6/storage/filesystem"

	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authz"
)

// Handler serves Git Smart HTTP using go-git v6's backend.Backend. It handles
// URL routing (/<namespace>/<project>.git/<rest>), authorization, and
// delegates the Git protocol to go-git's pure Go implementation.
type Handler struct {
	store   *Store
	backend *backend.Backend
	logger  *slog.Logger

	// PreReceive is called before a receive-pack is delegated to the backend.
	// It receives the namespace, project, principal, and parsed ref updates.
	// Return a non-nil error to reject the push with a 403 response.
	PreReceive func(namespace, project string, principal auth.Principal, refUpdates []RefUpdate) error

	// PostReceive is called after a successful receive-pack (push). It receives
	// the namespace, project, principal, and any push options extracted from the
	// protocol stream (e.g. "pull_request.create", "pull_request.target=main").
	PostReceive func(namespace, project string, principal auth.Principal, pushOpts []string)
}

// NewHandler returns a handler serving Git Smart HTTP for repos in store.
func NewHandler(store *Store, logger *slog.Logger) *Handler {
	h := &Handler{
		store:  store,
		logger: logger,
	}

	loader := &storeLoader{store: store}
	h.backend = &backend.Backend{
		Loader:   loader,
		ErrorLog: log.New(slogWriter{logger}, "", 0),
	}

	return h
}

// ServeHTTP routes /<namespace>/<project>.git/<rest>.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	// Parse: <namespace>/<project>.git/<rest>
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	namespace := parts[0]
	projectRaw := parts[1]

	project := strings.TrimSuffix(projectRaw, ".git")
	if project == projectRaw {
		http.NotFound(w, r)
		return
	}

	exists, err := h.store.RepoExists(namespace, project)
	if err != nil {
		h.logger.Error("repo check failed", "namespace", namespace, "project", project, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}

	// Authorization: clone/fetch = LevelRead, push = LevelWrite.
	level := h.requiredLevel(r, parts)
	if !h.authorize(w, r, namespace, project, level) {
		return
	}

	// For receive-pack POST, extract push options and ref updates from the
	// protocol stream before delegating to the backend.
	var pushOpts []string
	isReceivePack := len(parts) >= 3 && parts[2] == "git-receive-pack" && r.Method == http.MethodPost
	if isReceivePack {
		opts, refUpdates, newBody, err := ExtractReceivePackInfo(r.Body)
		if err != nil {
			h.logger.Error("receive-pack info extraction failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		r.Body = newBody
		pushOpts = opts

		if h.PreReceive != nil {
			principal, _ := auth.PrincipalFrom(r.Context())
			if err := h.PreReceive(namespace, project, principal, refUpdates); err != nil {
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}
	}

	// The URL path is already /<namespace>/<project>.git/<rest> — pass it
	// through to the backend's regex-based router.
	r2 := r.Clone(r.Context())
	r2.URL = &url.URL{
		Path:     "/" + path,
		RawQuery: r.URL.RawQuery,
	}
	// The backend's requireReceivePackAuth checks for an Authorization header on
	// receive-pack requests. GitYard handles auth at its own layer (above), so
	// inject a sentinel header to satisfy the backend's check when it's absent.
	if r2.Header.Get("Authorization") == "" {
		r2.Header = r2.Header.Clone()
		r2.Header.Set("Authorization", "Bearer gityard-internal")
	}
	// Attach push options to the context for downstream access.
	if len(pushOpts) > 0 {
		r2 = r2.WithContext(context.WithValue(r2.Context(), pushOptionKey{}, pushOpts))
	}

	h.backend.ServeHTTP(w, r2)

	// Post-receive hook: fire after a receive-pack POST completes.
	if isReceivePack && h.PostReceive != nil {
		principal, _ := auth.PrincipalFrom(r.Context())
		h.PostReceive(namespace, project, principal, pushOpts)
	}
}

func (h *Handler) requiredLevel(r *http.Request, parts []string) authz.Level {
	if len(parts) >= 3 {
		rest := parts[2]
		if rest == "git-receive-pack" {
			return authz.LevelWrite
		}
		if rest == "info/refs" && r.URL.Query().Get("service") == "git-receive-pack" {
			return authz.LevelWrite
		}
	}
	return authz.LevelRead
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, namespace, project string, level authz.Level) bool {
	principal, hasPrincipal := auth.PrincipalFrom(r.Context())
	scope := "*"
	if hasPrincipal {
		scope = principal.Scope
		if scope == "" {
			scope = "*"
		}
	}

	if err := authz.Authorize(scope, namespace, project, level); err != nil {
		if !hasPrincipal {
			w.Header().Set("WWW-Authenticate", `Basic realm="GitYard"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
		} else {
			http.Error(w, "permission denied", http.StatusForbidden)
		}
		return false
	}
	return true
}

// storeLoader implements transport.Loader, mapping URL paths to go-git storage.
type storeLoader struct {
	store *Store
}

func (l *storeLoader) Load(u *url.URL) (storage.Storer, error) {
	// URL path from the backend is /<namespace>/<project>.git (the regex captures
	// everything before /info/refs, /git-upload-pack, etc.).
	path := strings.TrimPrefix(u.Path, "/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 {
		return nil, transport.ErrRepositoryNotFound
	}

	namespace := parts[0]
	project := strings.TrimSuffix(parts[1], ".git")

	gitDir, err := l.store.GitDir(namespace, project)
	if err != nil {
		return nil, transport.ErrRepositoryNotFound
	}

	fs := osfs.New(filepath.Clean(gitDir), osfs.WithBoundOS())
	return filesystem.NewStorage(fs, cache.NewObjectLRUDefault()), nil
}

// IsGitRequest reports whether a URL path is a git transport request
// (matches /<namespace>/<project>.git/<rest>).
func IsGitRequest(path string) bool {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 2 {
		return false
	}
	return strings.HasSuffix(parts[1], ".git")
}

// BasicAuthMiddleware extracts a token from the HTTP Basic Auth password field
// (username is ignored — the standard git+PAT pattern) and attaches the resolved
// principal to the request context. When validateToken is nil (auth disabled),
// all requests pass through. When set and no credentials are provided, returns 401.
func BasicAuthMiddleware(validateToken func(string) (auth.Principal, auth.RejectReason, bool)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if validateToken == nil {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := auth.PrincipalFrom(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			_, password, ok := r.BasicAuth()
			if !ok || password == "" {
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

// slogWriter adapts slog.Logger to io.Writer for the backend's ErrorLog.
type slogWriter struct {
	logger *slog.Logger
}

func (w slogWriter) Write(p []byte) (int, error) {
	w.logger.Warn(strings.TrimSpace(string(p)), "source", "git-backend")
	return len(p), nil
}
