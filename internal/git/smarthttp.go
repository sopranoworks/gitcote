package git

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authz"
)

// SmartHTTPHandler serves Git Smart HTTP by delegating to git-http-backend (the
// CGI that ships with git). This is the Gitea/Gogs pattern — battle-tested,
// handles the full protocol (info/refs, upload-pack, receive-pack, push options).
//
// URL pattern: /git/<namespace>/<project>.git/<operation>
//
// Authentication is handled by the caller's middleware (the handler reads the
// principal from the request context). Authorization gates clone/fetch at
// LevelRead, push at LevelWrite.
type SmartHTTPHandler struct {
	store      *Store
	logger     *slog.Logger
	backendBin string

	// PostReceive is called after a successful receive-pack POST. It receives
	// the namespace, project, the authenticated principal (if any), and the
	// raw request body bytes (which include the ref updates and push options).
	// Step 2 logs only; step 3 will process push options.
	PostReceive func(namespace, project string, principal auth.Principal, info string)
}

// NewSmartHTTPHandler returns a handler serving Git Smart HTTP for repos in store.
func NewSmartHTTPHandler(store *Store, logger *slog.Logger) *SmartHTTPHandler {
	backend, err := exec.LookPath("git-http-backend")
	if err != nil {
		backend = gitHTTPBackendPath()
	}
	return &SmartHTTPHandler{
		store:      store,
		logger:     logger,
		backendBin: backend,
	}
}

// gitHTTPBackendPath returns the default path of git-http-backend using git --exec-path.
func gitHTTPBackendPath() string {
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		return "/usr/lib/git-core/git-http-backend"
	}
	return strings.TrimSpace(string(out)) + "/git-http-backend"
}

// ServeHTTP routes /git/<namespace>/<project>.git/<rest> to git-http-backend.
func (h *SmartHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/git/")
	if path == r.URL.Path {
		http.NotFound(w, r)
		return
	}

	// Split: <namespace>/<project>.git/<pathinfo>
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	namespace := parts[0]
	projectRaw := parts[1]
	rest := parts[2]

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

	// Authorize based on the operation.
	level := h.requiredLevel(r, rest)
	if !h.authorize(w, r, namespace, project, level) {
		return
	}

	repoPath, _ := h.store.RepoPath(namespace, project)

	// CGI environment for git-http-backend. The on-disk path has no .git suffix
	// (bare repos at <base>/<namespace>/<project>/), so PATH_INFO strips it.
	pathInfo := fmt.Sprintf("/%s/%s/%s", namespace, project, rest)
	env := []string{
		"GIT_PROJECT_ROOT=" + h.store.baseDir,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO=" + pathInfo,
		"QUERY_STRING=" + r.URL.RawQuery,
		"REQUEST_METHOD=" + r.Method,
		"CONTENT_TYPE=" + r.Header.Get("Content-Type"),
		"SERVER_PROTOCOL=HTTP/1.1",
		"PATH=" + os.Getenv("PATH"),
	}
	if r.ContentLength >= 0 {
		env = append(env, "CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10))
	}

	body := r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "bad gzip", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		body = gz
	}

	cmd := exec.CommandContext(r.Context(), h.backendBin)
	cmd.Env = env
	cmd.Stdin = body
	cmd.Dir = repoPath

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		h.logger.Error("git-http-backend start failed", "error", err)
		http.Error(w, "git error", http.StatusInternalServerError)
		return
	}

	// Parse CGI response headers, then stream the body.
	h.writeCGIResponse(w, stdout)

	if err := cmd.Wait(); err != nil {
		h.logger.Warn("git-http-backend exit", "error", err, "stderr", stderr.String())
	}

	// Post-receive hook point (in-process callback).
	if rest == "git-receive-pack" && r.Method == http.MethodPost && h.PostReceive != nil {
		principal, _ := auth.PrincipalFrom(r.Context())
		h.PostReceive(namespace, project, principal, stderr.String())
	}
}

// requiredLevel returns the authz level for a request based on the URL rest path
// and query parameters.
func (h *SmartHTTPHandler) requiredLevel(r *http.Request, rest string) authz.Level {
	switch {
	case rest == "git-receive-pack":
		return authz.LevelWrite
	case rest == "info/refs" && r.URL.Query().Get("service") == "git-receive-pack":
		return authz.LevelWrite
	default:
		return authz.LevelRead
	}
}

// authorize checks the request's principal against the required authz level.
func (h *SmartHTTPHandler) authorize(w http.ResponseWriter, r *http.Request, namespace, project string, level authz.Level) bool {
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

// writeCGIResponse parses CGI headers from the stdout pipe and writes the HTTP
// response (status, headers, body).
func (h *SmartHTTPHandler) writeCGIResponse(w http.ResponseWriter, stdout io.Reader) {
	scanner := bufio.NewReader(stdout)

	status := http.StatusOK
	for {
		line, err := scanner.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Status: ") {
			code, err := strconv.Atoi(strings.TrimSpace(line[8:11]))
			if err == nil {
				status = code
			}
			continue
		}
		if i := strings.IndexByte(line, ':'); i > 0 {
			w.Header().Set(strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]))
		}
		if err != nil {
			break
		}
	}

	w.WriteHeader(status)
	io.Copy(w, scanner)
}
