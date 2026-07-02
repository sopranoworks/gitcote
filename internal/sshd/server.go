// Package sshd implements an inbound SSH server for git transport.
package sshd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage"
	"github.com/go-git/go-git/v6/storage/filesystem"
	gossh "golang.org/x/crypto/ssh"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/sshkeys"
	"github.com/sopranoworks/shoka/pkg/auth"
)

type Server struct {
	store    *git.Store
	keyStore *sshkeys.Store
	config   *gossh.ServerConfig
	logger   *slog.Logger

	// PostReceive is called after a successful receive-pack push.
	PostReceive func(namespace, project string, principal auth.Principal, pushOpts []string)
}

func NewServer(store *git.Store, keyStore *sshkeys.Store, hostKeyPath string, logger *slog.Logger) (*Server, error) {
	hostKey, err := loadOrGenerateHostKey(hostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}

	s := &Server{
		store:    store,
		keyStore: keyStore,
		logger:   logger,
	}

	sshConfig := &gossh.ServerConfig{
		PublicKeyCallback: s.publicKeyCallback,
	}
	sshConfig.AddHostKey(hostKey)
	s.config = sshConfig

	return s, nil
}

func (s *Server) publicKeyCallback(conn gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
	email, found := s.keyStore.LookupByKey(key)
	if !found {
		return nil, fmt.Errorf("unknown public key")
	}
	return &gossh.Permissions{
		Extensions: map[string]string{
			"email": email,
		},
	}, nil
}

// Serve accepts SSH connections on the given listener until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.logger.Warn("ssh accept error", "error", err)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
}

func (s *Server) handleConn(ctx context.Context, netConn net.Conn) {
	defer netConn.Close()

	sshConn, chans, reqs, err := gossh.NewServerConn(netConn, s.config)
	if err != nil {
		s.logger.Debug("ssh handshake failed", "error", err)
		return
	}
	defer sshConn.Close()

	go gossh.DiscardRequests(reqs)

	email := sshConn.Permissions.Extensions["email"]
	s.logger.Info("ssh connection", "user", sshConn.User(), "email", email, "remote", sshConn.RemoteAddr())

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(gossh.UnknownChannelType, "unknown channel type")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ctx, ch, requests, email)
	}
}

func (s *Server) handleSession(ctx context.Context, ch gossh.Channel, reqs <-chan *gossh.Request, email string) {
	defer ch.Close()

	var envVars map[string]string
	for req := range reqs {
		switch req.Type {
		case "env":
			if envVars == nil {
				envVars = make(map[string]string)
			}
			var kv struct{ Name, Value string }
			if err := gossh.Unmarshal(req.Payload, &kv); err == nil {
				envVars[kv.Name] = kv.Value
			}
			if req.WantReply {
				req.Reply(true, nil)
			}

		case "exec":
			var cmd struct{ Command string }
			if err := gossh.Unmarshal(req.Payload, &cmd); err != nil {
				req.Reply(false, nil)
				return
			}
			req.Reply(true, nil)

			pushOpts := extractEnvPushOpts(envVars)
			exitCode := s.handleExec(ctx, ch, cmd.Command, email, pushOpts)
			sendExitStatus(ch, exitCode)
			return

		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (s *Server) handleExec(ctx context.Context, ch gossh.Channel, command, email string, pushOpts []string) uint32 {
	service, repoPath, err := parseGitCommand(command)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "error: %v\n", err)
		return 1
	}

	namespace, project, err := parseRepoPath(repoPath)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "error: %v\n", err)
		return 1
	}

	exists, err := s.store.RepoExists(namespace, project)
	if err != nil || !exists {
		fmt.Fprintf(ch.Stderr(), "error: repository not found\n")
		return 1
	}

	gitDir, err := s.store.GitDir(namespace, project)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "error: %v\n", err)
		return 1
	}

	fs := osfs.New(filepath.Clean(gitDir), osfs.WithBoundOS())
	storer := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())

	principal := auth.Principal{
		Email: email,
		Scope: "*",
	}

	reader := &readCloserWrapper{Reader: ch}
	writer := &channelWriteCloser{ch: ch}

	switch service {
	case "git-upload-pack":
		if err := transport.UploadPack(ctx, storer, reader, writer, &transport.UploadPackRequest{}); err != nil {
			s.logger.Warn("upload-pack error", "namespace", namespace, "project", project, "error", err)
			return 1
		}
		return 0

	case "git-receive-pack":
		return s.handleReceivePack(ctx, ch, storer, namespace, project, principal, pushOpts)

	default:
		fmt.Fprintf(ch.Stderr(), "error: unsupported service %q\n", service)
		return 1
	}
}

func (s *Server) handleReceivePack(ctx context.Context, ch gossh.Channel, st *filesystem.Storage, namespace, project string, principal auth.Principal, envPushOpts []string) uint32 {
	reader := &readCloserWrapper{Reader: ch}
	writer := &channelWriteCloser{ch: ch}

	// Wrap the storer with branch protection. go-git handles the full protocol;
	// ref updates are intercepted at the storer level and rejected if they
	// violate branch protection or allowed_branches restrictions.
	var protSt storage.Storer = st
	{
		repo, err := s.store.OpenRepo(namespace, project)
		if err != nil {
			s.logger.Warn("receive-pack open repo error", "error", err)
			fmt.Fprintf(ch.Stderr(), "error: %v\n", err)
			return 1
		}
		effectiveLevel := git.EffectiveGitLevel(principal.Scope, namespace, project)
		allowedBranches := git.AllowedBranchesFromExtra(principal.ExtraPermissions)
		protSt = &git.ProtectedStorer{
			Storer:  st,
			Repo:    repo,
			Level:   effectiveLevel,
			Allowed: allowedBranches,
		}
	}

	if err := transport.ReceivePack(ctx, protSt, reader, writer, &transport.ReceivePackRequest{}); err != nil {
		s.logger.Warn("receive-pack error", "namespace", namespace, "project", project, "error", err)
		return 1
	}

	if s.PostReceive != nil {
		s.PostReceive(namespace, project, principal, envPushOpts)
	}

	return 0
}


// readCloserWrapper wraps io.Reader with a no-op Close. The channel lifecycle
// is managed by the session handler, not by go-git's ReceivePack.
type readCloserWrapper struct {
	io.Reader
}

func (r *readCloserWrapper) Close() error { return nil }

// channelWriteCloser wraps an SSH channel for writing, using CloseWrite
// instead of Close to signal EOF on the write side only.
type channelWriteCloser struct {
	ch gossh.Channel
}

func (w *channelWriteCloser) Write(p []byte) (int, error) { return w.ch.Write(p) }
func (w *channelWriteCloser) Close() error                { return w.ch.CloseWrite() }

func extractEnvPushOpts(envVars map[string]string) []string {
	countStr, ok := envVars["GIT_PUSH_OPTIONS_COUNT"]
	if !ok {
		return nil
	}
	var count int
	fmt.Sscanf(countStr, "%d", &count)
	if count <= 0 {
		return nil
	}
	var opts []string
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("GIT_PUSH_OPTION_%d", i)
		if v, ok := envVars[key]; ok {
			opts = append(opts, v)
		}
	}
	return opts
}

func parseGitCommand(command string) (service, repoPath string, err error) {
	// Format: "git-upload-pack 'ns/proj.git'" or "git-receive-pack '/ns/proj.git'"
	parts := strings.SplitN(command, " ", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid git command: %q", command)
	}
	service = parts[0]
	repoPath = strings.Trim(parts[1], "'\"")
	switch service {
	case "git-upload-pack", "git-receive-pack":
	default:
		return "", "", fmt.Errorf("unsupported git command: %q", service)
	}
	return service, repoPath, nil
}

func parseRepoPath(path string) (namespace, project string, err error) {
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repository path: expected <namespace>/<project>.git")
	}
	return parts[0], parts[1], nil
}

func sendExitStatus(ch gossh.Channel, code uint32) {
	msg := gossh.Marshal(struct{ Status uint32 }{code})
	ch.SendRequest("exit-status", false, msg)
}

func loadOrGenerateHostKey(path string) (gossh.Signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return gossh.ParsePrivateKey(data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read host key: %w", err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}

	block, err := gossh.MarshalPrivateKey(priv, "gitcote host key")
	if err != nil {
		return nil, fmt.Errorf("marshal host key: %w", err)
	}

	pemData := pem.EncodeToMemory(block)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create host key dir: %w", err)
	}
	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		return nil, fmt.Errorf("write host key: %w", err)
	}

	return gossh.ParsePrivateKey(pemData)
}

