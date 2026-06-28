// Command gityard is the GitYard server. It boots the inherited Shoka core —
// userstore, oauthstore, the OAuth AS, auth middleware + authz gate, /auth/*,
// /ws/ui — and adds Git hosting via Smart HTTP (clone/fetch/push) using go-git v6
// (pure Go, no external binary), with MCP tools for project/repo administration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/agent"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/gityard/internal/integrity"
	"github.com/sopranoworks/gityard/internal/sshd"
	"github.com/sopranoworks/gityard/internal/sshkeys"
	"github.com/sopranoworks/gityard/internal/vault"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authapi"
	"github.com/sopranoworks/shoka/pkg/oauth"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
	"github.com/sopranoworks/shoka/pkg/reqtrace"
	"github.com/sopranoworks/shoka/pkg/serverurl"
	"github.com/sopranoworks/shoka/pkg/uiws"
	"github.com/sopranoworks/shoka/pkg/userstore"
	"golang.org/x/sync/errgroup"
	"gopkg.in/natefinch/lumberjack.v2"
)

const version = "0.0.3-step2r"

func main() {
	showVersion := flag.Bool("version", false, "Print the GitYard version and exit without starting the server.")
	configPath := flag.String("config", "gityard.yaml", "Path to configuration file")
	flag.Parse()

	if *showVersion {
		fmt.Printf("gityard %s\n", version)
		return
	}

	cfg, err := Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	logDest, logger, err := buildLogger(cfg.Server.Log)
	if err != nil {
		log.Fatalf("failed to configure logging: %v", err)
	}
	defer logDest.Close()
	slog.SetDefault(logger)

	if err := run(cfg, logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
	logger.Info("servers shut down gracefully")
}

func run(cfg *Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.Storage.BaseDir, 0o700); err != nil {
		return fmt.Errorf("create storage base dir: %w", err)
	}

	// ---- User store (B-28 multi-user login backing /auth/* and /ws/ui) ----
	totpKey, err := userstore.ResolveTOTPKey(
		cfg.Server.Auth.Users.TOTPEncryptionKey,
		filepath.Join(cfg.Storage.BaseDir, "userstore.key"),
	)
	if err != nil {
		return fmt.Errorf("resolve user-store TOTP key: %w", err)
	}
	userStore, err := userstore.Open(filepath.Join(cfg.Storage.BaseDir, "users.db"), totpKey)
	if err != nil {
		return fmt.Errorf("open user store: %w", err)
	}
	defer func() { _ = userStore.Close() }()

	// ---- OAuth authorization server (enabled by oauth transport presence) ----
	var (
		oauthStore *oauthstore.Store
		authServer *oauth.AuthServer
		authConfig auth.Config
	)
	oauthEnabled := cfg.OAuthEnabled()
	discoveryCfg := oauth.DiscoveryConfig{
		ExternalURL:      cfg.Server.MCP.OAuth.ExternalURL,
		RegistrationMode: oauth.RegistrationMode(cfg.Server.MCP.OAuth.RegistrationModeOrDefault()),
		Logger:           logger,
	}
	if oauthEnabled {
		// The RFC 9728 challenge composer: where to find the Protected Resource
		// Metadata, composed per-request so forwarded headers can drive it.
		authConfig.ResourceMetadataURL = func(r *http.Request) string {
			base, err := serverurl.Base(discoveryCfg.ExternalURL, r)
			if err != nil {
				return ""
			}
			return serverurl.ProtectedResourceMetadataURL(base)
		}
		if cfg.Server.MCP.OAuth.ExternalURL == "" {
			logger.Warn("oauth transport configured without server.mcp.oauth.external_url; " +
				"relying on per-request X-Forwarded-* headers to compose the public URL")
		}

		oauthStore, err = oauthstore.Open(filepath.Join(cfg.Storage.BaseDir, "oauth.db"))
		if err != nil {
			return fmt.Errorf("open oauth token store: %w", err)
		}
		defer func() { _ = oauthStore.Close() }()

		// Periodic dead-series GC (the only GC the OAuth store has). ON by default.
		oauthStore.StartCleaner(ctx, oauthstore.CleanerConfig{Enabled: true, Logger: logger})

		// Trust source is the dynamic domain store (managed via /ws/ui DOMAIN_* ops).
		verifier := oauth.NewVerifier(nil)
		verifier.SetTrustedSource(oauthStore.TrustedDomain)
		authServer = oauth.NewAuthServer(oauthStore, verifier, oauth.AuthServerConfig{
			ExternalURL: cfg.Server.MCP.OAuth.ExternalURL,
			BoundPrincipal: oauthstore.Principal{
				Name:  cfg.Identity.User.Name,
				Email: cfg.Identity.User.Email,
			},
			Logger: logger,
		})

		// Token enforcement on the MCP path: a valid access token is required and its
		// bound principal is attached to the request.
		authConfig.ValidateToken = func(token string) (auth.Principal, auth.RejectReason, bool) {
			if token == "" {
				return auth.Principal{}, auth.ReasonMissingBearer, false
			}
			rec, lerr := oauthStore.Lookup(token, time.Now())
			if lerr != nil {
				reason := auth.ReasonInvalidToken
				if errors.Is(lerr, oauthstore.ErrExpired) {
					reason = auth.ReasonExpired
				}
				return auth.Principal{}, reason, false
			}
			if rec.Principal.Name == "" {
				return auth.Principal{}, auth.ReasonPrincipalUnresolved, false
			}
			scope := rec.Scope
			if scope == "" {
				scope = "*"
			}
			p := auth.Principal{Name: rec.Principal.Name, Email: rec.Principal.Email, ClientID: rec.ClientID, Scope: scope, ExtraPermissions: rec.ExtraPermissions}
			if IsAgentToken(token) {
				RecordAgentActivity(token)
			}
			return p, "", true
		}
	}

	// ---- Web/non-MCP authenticator (static-bearer + WS origin policy ONLY) ----
	// Never carries the OAuth ValidateToken closure: an OAuth access token is an
	// MCP-client credential and must not gate a browser route (Shoka B-50 separation).
	webAuth := auth.New(auth.Config{
		Enabled:        cfg.Server.Auth.Enabled,
		Tokens:         cfg.Server.Auth.Tokens,
		AllowedOrigins: cfg.Server.Auth.AllowedOrigins,
		Logger:         logger,
	})

	// ---- /auth/* login surface (password + TOTP; WebAuthn deferred) ----
	authHandler := authapi.New(authapi.Config{
		Users:              userStore,
		RPDisplayName:      "GitYard",
		SessionTTL:         cfg.Server.Auth.Users.SessionTTL.Or(720 * time.Hour),
		AllowFirstRunAdmin: cfg.Server.Auth.Users.FirstRunAdminAllowed(),
		Logger:             logger,
	})

	// ---- Git repository store (non-bare, .git/ layout) ----
	gitStore := git.NewStore(cfg.Storage.BaseDir)

	// ---- SSH key vault (namespace-scoped, encrypted at rest) ----
	keyVault, err := vault.Open(filepath.Join(cfg.Storage.BaseDir, "keys.db"))
	if err != nil {
		return fmt.Errorf("open key vault: %w", err)
	}
	defer func() { _ = keyVault.Close() }()

	gityardURL := cfg.Server.HTTP.ExternalURL
	if gityardURL == "" {
		gityardURL = "http://" + cfg.Server.HTTP.Listen
	}

	seedCtx := &seedContext{
		gitStore:   gitStore,
		vault:      keyVault,
		userStore:  userStore,
		gityardURL: gityardURL,
	}

	// ---- Repository integrity check (HEAD hash store) ----
	var integrityHS *integrity.Store
	integrityHS, err = integrity.Open(filepath.Join(cfg.Storage.BaseDir, "repo_heads.db"))
	if err != nil {
		return fmt.Errorf("open integrity store: %w", err)
	}
	defer func() { _ = integrityHS.Close() }()
	headStore = integrityHS

	evtCtx := &eventContext{
		gitStore:    gitStore,
		integrityHS: integrityHS,
		oauthStore:  oauthStore,
		agentCfg:    cfg.AgentSpawn,
		gityardURL:  gityardURL,
		seedCtx:     seedCtx,
		logger:      logger,
	}

	// ---- Smart HTTP handler (/<ns>/<proj>.git/...) — pure Go via go-git v6 ----
	gitHTTP := git.NewHandler(gitStore, logger)
	gitHTTP.PreReceive = func(namespace, project string, principal auth.Principal, refUpdates []git.RefUpdate) error {
		return checkBranchProtection(gitStore, namespace, project, principal, refUpdates)
	}
	gitHTTP.PostReceive = func(namespace, project string, principal auth.Principal, pushOpts []string) {
		logger.Info("post-receive",
			"namespace", namespace, "project", project,
			"principal", principal.Email, "push_options", pushOpts)
		handlePostReceive(gitStore, logger, namespace, project, principal, pushOpts, evtCtx)
	}

	// ---- /ws/ui user/OAuth management (Shoka core handlers, GitYard ws wrapper) ----
	core := &uiws.CoreHandlers{}
	core.SetUserStore(userStore)
	if oauthStore != nil {
		core.SetOAuthStore(oauthStore)
		// Token-to-self: mint a CLI access token for the operator (OAUTH_ISSUE_SELF).
		core.SetOAuthSelfIssuer(uiws.OAuthSelfIssuerFunc(func(r *http.Request, accessTTL time.Duration, extraPermissions map[string]any) (string, time.Time, error) {
			base, berr := serverurl.Base(cfg.Server.MCP.OAuth.ExternalURL, r)
			if berr != nil {
				return "", time.Time{}, berr
			}
			if accessTTL <= 0 {
				accessTTL = time.Hour
			}
			rec, nerr := oauthStore.NewSeries(
				oauthstore.SelfIssuedClientID,
				oauthstore.Principal{Name: cfg.Identity.User.Name, Email: cfg.Identity.User.Email},
				serverurl.ResourceURL(base),
				"*",
				time.Now(),
				accessTTL,
				accessTTL,
				extraPermissions,
			)
			if nerr != nil {
				return "", time.Time{}, nerr
			}
			return rec.AccessToken, rec.AccessExpiry, nil
		}))
	}
	// ---- User SSH public key store (for inbound SSH auth) ----
	sshKeyStore, err := sshkeys.Open(filepath.Join(cfg.Storage.BaseDir, "ssh_authorized_keys.db"))
	if err != nil {
		return fmt.Errorf("open SSH key store: %w", err)
	}
	defer func() { _ = sshKeyStore.Close() }()

	integrityStatus := &IntegrityStatus{}

	srvInfoCtx := &serverInfoContext{
		httpListen:      cfg.Server.HTTP.Listen,
		httpExternalURL: cfg.Server.HTTP.ExternalURL,
		mcpPlainListen:  cfg.Server.MCP.Plain.Listen,
		mcpOAuthListen:  cfg.Server.MCP.OAuth.Listen,
		mcpOAuthExtURL:  cfg.Server.MCP.OAuth.ExternalURL,
		sshListen:        cfg.Server.SSH.Listen,
		sshExternalURL:   cfg.Server.SSH.ExternalURL,
		integrityStatus:  integrityStatus,
	}

	wsMgr := newWSManager(core, webAuth.OriginAllowed, seedCtx, gitStore, sshKeyStore, cfg.Server.SSH.Listen, srvInfoCtx, integrityHS, evtCtx, logger)

	// ---- Agent spawn: ensure default configs exist ----
	if cfg.AgentSpawn.IsEnabled() {
		agentConfigRoot := cfg.AgentSpawn.EffectiveConfigRoot(cfg.Storage.BaseDir)
		if err := agent.EnsureDefaultAgents(agentConfigRoot); err != nil {
			logger.Warn("failed to ensure default agent configs", "error", err)
		}
	}

	// ---- MCP server (Git management tools + server info) ----
	mcpServer := setupMCPServer(cfg, gitStore, seedCtx, oauthStore, gityardURL, integrityHS, evtCtx, logger)

	// ---- Seed push scheduler (periodic mode) ----
	startSeedScheduler(ctx, seedCtx, logger)

	// ---- Repository integrity check worker ----
	startIntegrityWorker(ctx, gitStore, integrityHS, cfg.Server.IntegrityCheck, integrityStatus, func(alert IntegrityAlert) {
		wsMgr.Broadcast(MsgRepoIntegrityAlert, alert)
	}, logger)

	// ---- Seed temp clone cleanup ----
	startTempCloneCleanup(ctx, integrityHS, logger)

	// ---- HTTP listeners ----
	g, ctx := errgroup.WithContext(ctx)

	// Git auth: tokens are validated from the HTTP Basic Auth password field.
	// When OAuth is enabled, use the OAuth ValidateToken closure. When OAuth is
	// disabled but static-bearer auth is enabled, validate against the static
	// tokens list. When auth is fully disabled, pass nil (all requests proceed).
	var gitValidateToken func(string) (auth.Principal, auth.RejectReason, bool)
	if authConfig.ValidateToken != nil {
		gitValidateToken = authConfig.ValidateToken
	} else if cfg.Server.Auth.Enabled && len(cfg.Server.Auth.Tokens) > 0 {
		tokens := cfg.Server.Auth.Tokens
		gitValidateToken = func(token string) (auth.Principal, auth.RejectReason, bool) {
			for _, t := range tokens {
				if t == token {
					return auth.Principal{
						Name:  cfg.Identity.User.Name,
						Email: cfg.Identity.User.Email,
						Scope: "*",
					}, "", true
				}
			}
			return auth.Principal{}, auth.ReasonInvalidToken, false
		}
	}

	dumpHTTP := cfg.Server.Debug.DumpHTTP

	// Trusted networks: IP-based auth bypass for internal deployments.
	trustedNets := parseCIDRs(cfg.Server.HTTP.TrustedNetworks)
	trustedProxies := parseCIDRs(cfg.Server.HTTP.TrustedProxies)
	trustedPrincipal := auth.Principal{
		Name:  cfg.Identity.User.Name,
		Email: cfg.Identity.User.Email,
		Scope: "*",
	}

	// Web listener: /auth/*, /ws/ui, /git/*, static (none yet). The whole mux is wrapped
	// by authHandler.Middleware so the session principal is resolved for every route.
	// TrustedNetworkMiddleware sits outermost: trusted IPs get a synthetic principal
	// that satisfies all downstream auth layers.
	webHandler := TrustedNetworkMiddleware(trustedNets, trustedProxies, trustedPrincipal)(
		reqtrace.Middleware(logger, "web", dumpHTTP)(setupWebHandler(webAuth, authHandler, wsMgr, gitHTTP, gitValidateToken)))
	g.Go(func() error {
		return runServer(ctx, "web", cfg.Server.HTTP.Listen, webHandler, logger)
	})

	newMCPHandler := func() http.Handler {
		return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer },
			&mcp.StreamableHTTPOptions{Logger: logger})
	}

	// Plain (internal) MCP transport: static-bearer iff bearer_auth, else open loopback.
	if cfg.Server.MCP.Plain.Listen != "" {
		var plainAuth *auth.Authenticator
		if cfg.Server.MCP.Plain.BearerAuth {
			plainAuth = auth.New(auth.Config{Enabled: true, Tokens: cfg.Server.Auth.Tokens, Logger: logger})
		} else {
			plainAuth = auth.New(auth.Config{}) // disabled → allow all (loopback use)
		}
		handler := reqtrace.Middleware(logger, "mcp-plain", dumpHTTP)(plainAuth.Middleware(newMCPHandler()))
		g.Go(func() error {
			return runServer(ctx, "mcp-plain", cfg.Server.MCP.Plain.Listen, handler, logger)
		})
	}

	// OAuth (external) MCP transport: discovery + /authorize + /token unauthenticated,
	// the MCP handler behind OAuth token enforcement.
	if oauthEnabled {
		oauthAuth := auth.New(auth.Config{
			ResourceMetadataURL: authConfig.ResourceMetadataURL,
			ValidateToken:       authConfig.ValidateToken,
			Logger:              logger,
		})
		handler := reqtrace.Middleware(logger, "mcp-oauth", dumpHTTP)(oauthListenerHandler(discoveryCfg, authServer, newMCPHandler(), oauthAuth))
		g.Go(func() error {
			return runServer(ctx, "mcp-oauth", cfg.Server.MCP.OAuth.Listen, handler, logger)
		})
	}

	// ---- Inbound SSH server (git transport via SSH keys) ----
	if cfg.Server.SSH.Listen != "" {
		hostKeyPath := cfg.Server.SSH.HostKeyPath
		if hostKeyPath == "" {
			hostKeyPath = "ssh_host_ed25519_key"
		}
		if !filepath.IsAbs(hostKeyPath) {
			hostKeyPath = filepath.Join(cfg.Storage.BaseDir, hostKeyPath)
		}
		sshServer, serr := sshd.NewServer(gitStore, sshKeyStore, hostKeyPath, logger)
		if serr != nil {
			return fmt.Errorf("create SSH server: %w", serr)
		}
		sshServer.PostReceive = func(namespace, project string, principal auth.Principal, pushOpts []string) {
			logger.Info("ssh post-receive",
				"namespace", namespace, "project", project,
				"principal", principal.Email, "push_options", pushOpts)
			handlePostReceive(gitStore, logger, namespace, project, principal, pushOpts, evtCtx)
		}
		g.Go(func() error {
			ln, lerr := net.Listen("tcp", cfg.Server.SSH.Listen)
			if lerr != nil {
				return fmt.Errorf("ssh listen: %w", lerr)
			}
			logger.Info("starting server", "name", "ssh", "addr", cfg.Server.SSH.Listen)
			return sshServer.Serve(ctx, ln)
		})
	}

	return g.Wait()
}

// setupWebHandler builds the Web mux: the /auth/* login surface, the /ws/ui
// management WebSocket, and the Smart HTTP handler (/<ns>/<proj>.git/...). The
// .git suffix disambiguates git requests from frontend routes. /ws/ui takes the
// ?token= query fallback and additionally requires a login session once a user
// exists. Git transport uses HTTP Basic Auth with the token in the password field.
// The whole mux is wrapped by authHandler.Middleware so the session principal is
// attached to every route (excluding git, which has its own auth layer).
func setupWebHandler(webAuth *auth.Authenticator, authHandler *authapi.Handler, wsMgr http.Handler, gitHTTP http.Handler, validateToken func(string) (auth.Principal, auth.RejectReason, bool)) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/ws/ui", webAuth.MiddlewareAllowQueryToken(authHandler.RequireSession(wsMgr)))
	mux.Handle("/auth/", authHandler)

	gitAuth := git.BasicAuthMiddleware(validateToken)(gitHTTP)
	frontendFS, err := fs.Sub(distFS, "dist")
	var fileServer http.Handler
	if err == nil {
		fileServer = http.FileServer(http.FS(frontendFS))
	}

	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if git.IsGitRequest(r.URL.Path) {
			gitAuth.ServeHTTP(w, r)
			return
		}
		if fileServer == nil {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, ferr := frontendFS.Open(path); ferr != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	}))

	return authHandler.Middleware(mux)
}

// setupMCPServer builds the MCP server with GitYard's tool surface: server info,
// project/repo management (create_project, list_projects).
func setupMCPServer(cfg *Config, gitStore *git.Store, sc *seedContext, oauthStore *oauthstore.Store, gityardURL string, integrityHS *integrity.Store, ec *eventContext, logger *slog.Logger) *mcp.Server {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "gityard", Version: version},
		&mcp.ServerOptions{Logger: logger},
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "get_server_info",
		Description: "Get information about the GitYard server (name, version, public URL).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ serverInfoInput) (*mcp.CallToolResult, serverInfoOutput, error) {
		return nil, serverInfoOutput{
			Name:        "gityard",
			Version:     version,
			ExternalURL: cfg.Server.HTTP.ExternalURL,
		}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "create_project",
		Description: "Create a new bare Git repository under a namespace. Optionally clone from a seed URL (HTTP or SSH). SSH clone uses the namespace's deploy key.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in createProjectInput) (*mcp.CallToolResult, createProjectOutput, error) {
		if in.CloneURL != "" {
			if isSSHURL(in.CloneURL) {
				pemData, kerr := resolveDeployKey(sc, in.Namespace, in.KeyName)
				if kerr != nil {
					return nil, createProjectOutput{}, kerr
				}
				if err := gitStore.CloneRepoSSH(in.Namespace, in.ProjectName, in.CloneURL, pemData); err != nil {
					return nil, createProjectOutput{}, fmt.Errorf("clone (SSH): %w", err)
				}
			} else {
				if err := gitStore.CloneRepo(in.Namespace, in.ProjectName, in.CloneURL); err != nil {
					return nil, createProjectOutput{}, fmt.Errorf("clone: %w", err)
				}
			}
		} else {
			if err := gitStore.CreateRepo(in.Namespace, in.ProjectName); err != nil {
				return nil, createProjectOutput{}, fmt.Errorf("create: %w", err)
			}
		}
		return nil, createProjectOutput{
			Namespace: in.Namespace,
			Project:   in.ProjectName,
			Message:   "project created",
		}, nil
	})

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "list_projects",
		Description: "List all projects (Git repositories), optionally scoped to a namespace.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listProjectsInput) (*mcp.CallToolResult, listProjectsOutput, error) {
		projects, err := gitStore.ListProjects(in.Namespace)
		if err != nil {
			return nil, listProjectsOutput{}, err
		}
		return nil, listProjectsOutput{Projects: projects}, nil
	})

	registerPRTools(mcpServer, gitStore, sc, ec)
	registerRepoTools(mcpServer, gitStore)
	registerSeedTools(mcpServer, gitStore, sc.vault, gityardURL)
	registerTokenTools(mcpServer, gitStore, oauthStore, cfg.Server.HTTP.ExternalURL, cfg.Server.HTTP.Listen)
	registerAgentTools(mcpServer, gitStore, cfg.AgentSpawn, cfg.Storage.BaseDir, gityardURL, integrityHS, logger)

	return mcpServer
}

type serverInfoInput struct{}

type serverInfoOutput struct {
	Name        string `json:"name" jsonschema:"the server name"`
	Version     string `json:"version" jsonschema:"the server version"`
	ExternalURL string `json:"external_url,omitempty" jsonschema:"the configured public URL, if any"`
}

type createProjectInput struct {
	Namespace   string `json:"namespace" jsonschema:"the namespace (defaults to 'default' if empty)"`
	ProjectName string `json:"project_name" jsonschema:"required,the project name ([a-zA-Z0-9_-]+)"`
	CloneURL    string `json:"clone_url,omitempty" jsonschema:"optional seed URL to clone from (HTTP or SSH)"`
	KeyName     string `json:"key_name,omitempty" jsonschema:"deploy key name for SSH clone (uses first key if omitted)"`
}

func isSSHURL(u string) bool {
	return strings.HasPrefix(u, "git@") || strings.HasPrefix(u, "ssh://")
}

func resolveDeployKey(sc *seedContext, namespace, keyName string) ([]byte, error) {
	if sc.vault.State() != vault.VaultUnlocked {
		return nil, fmt.Errorf("vault is locked — resume required for SSH clone")
	}
	if keyName != "" {
		return sc.vault.DecryptPrivateKey(namespace, keyName)
	}
	keys, err := sc.vault.ListKeys(namespace)
	if err != nil {
		return nil, fmt.Errorf("list deploy keys: %w", err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no deploy keys in namespace %q — import or generate one first", namespace)
	}
	return sc.vault.DecryptPrivateKey(namespace, keys[0].Name)
}

type createProjectOutput struct {
	Namespace string `json:"namespace"`
	Project   string `json:"project"`
	Message   string `json:"message"`
}

type listProjectsInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"optional namespace filter"`
}

type listProjectsOutput struct {
	Projects []git.ProjectInfo `json:"projects"`
}

// oauthListenerHandler assembles the OAuth MCP listener: discovery documents and the
// /authorize + /token endpoints WITHOUT auth (they must be reachable before a token
// exists), and the OAuth-enforcing MCP handler as the "/" catch-all (the handler is
// path-agnostic, so it serves /mcp and elsewhere).
func oauthListenerHandler(discoveryCfg oauth.DiscoveryConfig, authServer *oauth.AuthServer, mcpHandler http.Handler, authenticator *auth.Authenticator) http.Handler {
	mux := http.NewServeMux()
	oauth.RegisterDiscovery(mux, discoveryCfg)
	if authServer != nil {
		authServer.RegisterEndpoints(mux)
	}
	mux.Handle("/", authenticator.Middleware(mcpHandler))
	return mux
}

// logDestination wraps an io.Writer with an optional Close method.
type logDestination struct {
	io.Writer
	closer io.Closer
}

func (d *logDestination) Close() error {
	if d.closer != nil {
		return d.closer.Close()
	}
	return nil
}

// buildLogger constructs the slog.Logger from config, returning the destination
// (which must be closed on shutdown) and the logger.
func buildLogger(cfg LogConfig) (*logDestination, *slog.Logger, error) {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(cfg.Level)) {
	case "", "info":
		lvl = slog.LevelInfo
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, nil, fmt.Errorf("invalid log level %q", cfg.Level)
	}

	var w io.Writer
	dest := &logDestination{}
	switch strings.ToLower(strings.TrimSpace(cfg.Output)) {
	case "", "stderr":
		w = os.Stderr
	case "file":
		if cfg.File.Path == "" {
			return nil, nil, fmt.Errorf("server.log.file.path is required when output is \"file\"")
		}
		dir := filepath.Dir(cfg.File.Path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("create log dir %q: %w", dir, err)
		}
		maxSize := cfg.File.MaxSizeMB
		if maxSize <= 0 {
			maxSize = 100
		}
		maxBackups := cfg.File.MaxBackups
		if maxBackups <= 0 {
			maxBackups = 7
		}
		maxAge := cfg.File.MaxAgeDays
		if maxAge <= 0 {
			maxAge = 30
		}
		lj := &lumberjack.Logger{
			Filename:   cfg.File.Path,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge,
			Compress:   cfg.File.Compress,
		}
		w = lj
		dest.closer = lj
	default:
		return nil, nil, fmt.Errorf("invalid log output %q", cfg.Output)
	}
	dest.Writer = w

	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "", "text":
		h = slog.NewTextHandler(w, opts)
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		return nil, nil, fmt.Errorf("invalid log format %q", cfg.Format)
	}

	return dest, slog.New(h), nil
}

// runServer runs one HTTP listener until ctx is cancelled, then shuts it down
// gracefully. GitYard terminates no TLS by design (sit behind a TLS-terminating proxy).
func runServer(ctx context.Context, name, addr string, handler http.Handler, logger *slog.Logger) error {
	srv := &http.Server{Addr: addr, Handler: handler}
	errChan := make(chan error, 1)
	go func() {
		logger.Info("starting server", "name", name, "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down server", "name", name)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errChan:
		return err
	}
}
