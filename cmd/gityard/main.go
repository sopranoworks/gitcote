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
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sopranoworks/gityard/internal/git"
	"github.com/sopranoworks/shoka/pkg/auth"
	"github.com/sopranoworks/shoka/pkg/authapi"
	"github.com/sopranoworks/shoka/pkg/oauth"
	"github.com/sopranoworks/shoka/pkg/oauthstore"
	"github.com/sopranoworks/shoka/pkg/serverurl"
	"github.com/sopranoworks/shoka/pkg/uiws"
	"github.com/sopranoworks/shoka/pkg/userstore"
	"golang.org/x/sync/errgroup"
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

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
		RegistrationMode: oauth.RegistrationMode("dcr"),
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
			AccessTTL:  cfg.OAuth.AccessTokenTTL.Or(time.Hour),
			RefreshTTL: cfg.OAuth.RefreshTokenTTL.Or(720 * time.Hour),
			CodeTTL:    cfg.OAuth.AuthorizationCodeTTL.Or(5 * time.Minute),
			Logger:     logger,
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
			return auth.Principal{Name: rec.Principal.Name, Email: rec.Principal.Email, ClientID: rec.ClientID, Scope: scope}, "", true
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

	// ---- Smart HTTP handler (/git/<ns>/<proj>.git/...) — pure Go via go-git v6 ----
	gitHTTP := git.NewHandler(gitStore, logger)
	gitHTTP.PostReceive = func(namespace, project string, principal auth.Principal) {
		logger.Info("post-receive",
			"namespace", namespace, "project", project,
			"principal", principal.Email)
	}

	// ---- /ws/ui user/OAuth management (Shoka core handlers, GitYard ws wrapper) ----
	core := &uiws.CoreHandlers{}
	core.SetUserStore(userStore)
	if oauthStore != nil {
		core.SetOAuthStore(oauthStore)
		// Token-to-self: mint a CLI access token for the operator (OAUTH_ISSUE_SELF).
		core.SetOAuthSelfIssuer(uiws.OAuthSelfIssuerFunc(func(r *http.Request, accessTTL time.Duration) (string, time.Time, error) {
			base, berr := serverurl.Base(cfg.Server.MCP.OAuth.ExternalURL, r)
			if berr != nil {
				return "", time.Time{}, berr
			}
			if accessTTL <= 0 {
				accessTTL = cfg.OAuth.AccessTokenTTL.Or(time.Hour)
			}
			rec, nerr := oauthStore.NewSeries(
				oauthstore.SelfIssuedClientID,
				oauthstore.Principal{Name: cfg.Identity.User.Name, Email: cfg.Identity.User.Email},
				serverurl.ResourceURL(base),
				"*",
				time.Now(),
				accessTTL,
				accessTTL,
			)
			if nerr != nil {
				return "", time.Time{}, nerr
			}
			return rec.AccessToken, rec.AccessExpiry, nil
		}))
	}
	wsMgr := newWSManager(core, webAuth.OriginAllowed, logger)

	// ---- MCP server (Git management tools + server info) ----
	mcpServer := setupMCPServer(cfg, gitStore, logger)

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

	// Web listener: /auth/*, /ws/ui, /git/*, static (none yet). The whole mux is wrapped
	// by authHandler.Middleware so the session principal is resolved for every route.
	webHandler := setupWebHandler(webAuth, authHandler, wsMgr, gitHTTP, gitValidateToken)
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
		handler := plainAuth.Middleware(newMCPHandler())
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
		handler := oauthListenerHandler(discoveryCfg, authServer, newMCPHandler(), oauthAuth)
		g.Go(func() error {
			return runServer(ctx, "mcp-oauth", cfg.Server.MCP.OAuth.Listen, handler, logger)
		})
	}

	return g.Wait()
}

// setupWebHandler builds the Web mux: the /auth/* login surface, the /ws/ui
// management WebSocket, and the /git/ Smart HTTP handler. /ws/ui takes the ?token=
// query fallback (browsers cannot set an Authorization header on a WS handshake) and
// additionally requires a login session once a user exists (RequireSession passes
// through while the store is empty — no-lockout). The /git/ path uses HTTP Basic Auth
// with the token in the password field (the standard git+PAT pattern). The whole mux
// is wrapped by authHandler.Middleware so the session principal is attached to every
// route (excluding /git/, which has its own auth layer).
func setupWebHandler(webAuth *auth.Authenticator, authHandler *authapi.Handler, wsMgr http.Handler, gitHTTP http.Handler, validateToken func(string) (auth.Principal, auth.RejectReason, bool)) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/ws/ui", webAuth.MiddlewareAllowQueryToken(authHandler.RequireSession(wsMgr)))
	mux.Handle("/auth/", authHandler)
	mux.Handle("/git/", git.BasicAuthMiddleware(validateToken)(gitHTTP))
	return authHandler.Middleware(mux)
}

// setupMCPServer builds the MCP server with GitYard's tool surface: server info,
// project/repo management (create_project, list_projects).
func setupMCPServer(cfg *Config, gitStore *git.Store, logger *slog.Logger) *mcp.Server {
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
		Description: "Create a new bare Git repository under a namespace. Optionally clone from a seed URL.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in createProjectInput) (*mcp.CallToolResult, createProjectOutput, error) {
		if in.CloneURL != "" {
			if err := gitStore.CloneRepo(in.Namespace, in.ProjectName, in.CloneURL); err != nil {
				return nil, createProjectOutput{}, fmt.Errorf("clone: %w", err)
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
		Description: "List all projects (bare Git repositories), optionally scoped to a namespace.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in listProjectsInput) (*mcp.CallToolResult, listProjectsOutput, error) {
		projects, err := gitStore.ListProjects(in.Namespace)
		if err != nil {
			return nil, listProjectsOutput{}, err
		}
		return nil, listProjectsOutput{Projects: projects}, nil
	})

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
	CloneURL    string `json:"clone_url,omitempty" jsonschema:"optional seed URL to clone from"`
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
