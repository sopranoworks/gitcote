package sshd_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"github.com/sopranoworks/gitcote/internal/git"
	"github.com/sopranoworks/gitcote/internal/sshd"
	"github.com/sopranoworks/gitcote/internal/sshkeys"
)

func TestSSHCloneAndPush(t *testing.T) {
	env := setupSSHEnv(t)

	// Clone via SSH.
	cloneDir := t.TempDir()
	sshCmd := sshCommand(env.keyFile, env.port)
	runGitSSH(t, cloneDir, sshCmd, "clone", env.cloneURL("ns", "proj"), "repo")
	repoDir := filepath.Join(cloneDir, "repo")

	// Push initial commit to main.
	os.WriteFile(filepath.Join(repoDir, "hello.txt"), []byte("hello\n"), 0o644)
	runGitSSH(t, repoDir, sshCmd, "add", "hello.txt")
	runGitSSH(t, repoDir, sshCmd, "commit", "-m", "initial commit")
	runGitSSH(t, repoDir, sshCmd, "push", "-u", "origin", "HEAD:refs/heads/main")

	// Verify by cloning again.
	verifyDir := t.TempDir()
	runGitSSH(t, verifyDir, sshCmd, "clone", env.cloneURL("ns", "proj"), "verify")
	content, err := os.ReadFile(filepath.Join(verifyDir, "verify", "hello.txt"))
	if err != nil {
		t.Fatalf("read hello.txt: %v", err)
	}
	if string(content) != "hello\n" {
		t.Errorf("content = %q, want %q", string(content), "hello\n")
	}
}

func TestSSHUnknownKeyRejected(t *testing.T) {
	env := setupSSHEnv(t)

	// Generate a different key not registered in the store.
	_, unknownPriv, _ := ed25519.GenerateKey(rand.Reader)
	unknownKeyFile := filepath.Join(t.TempDir(), "unknown_key")
	writeSSHPrivateKey(t, unknownKeyFile, unknownPriv)

	cloneDir := t.TempDir()
	sshCmd := sshCommand(unknownKeyFile, env.port)
	err := runGitSSHResult(cloneDir, sshCmd, "clone", env.cloneURL("ns", "proj"), "repo")
	if err == nil {
		t.Fatal("clone with unknown key should fail")
	}
}

func TestSSHBranchProtection(t *testing.T) {
	env := setupSSHEnv(t)
	sshCmd := sshCommand(env.keyFile, env.port)

	// Push initial commit to main.
	cloneDir := t.TempDir()
	runGitSSH(t, cloneDir, sshCmd, "clone", env.cloneURL("ns", "proj"), "repo")
	repoDir := filepath.Join(cloneDir, "repo")
	os.WriteFile(filepath.Join(repoDir, "init.txt"), []byte("init\n"), 0o644)
	runGitSSH(t, repoDir, sshCmd, "add", "init.txt")
	runGitSSH(t, repoDir, sshCmd, "commit", "-m", "init")
	runGitSSH(t, repoDir, sshCmd, "push", "-u", "origin", "HEAD:refs/heads/main")

	// Push to feature branch → should succeed (scope=* = W-level).
	runGitSSH(t, repoDir, sshCmd, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("feature\n"), 0o644)
	runGitSSH(t, repoDir, sshCmd, "add", "feature.txt")
	runGitSSH(t, repoDir, sshCmd, "commit", "-m", "feature")
	runGitSSH(t, repoDir, sshCmd, "push", "-u", "origin", "HEAD:refs/heads/feature")

	// Force push to main → should be rejected.
	runGitSSH(t, repoDir, sshCmd, "checkout", "main")
	os.WriteFile(filepath.Join(repoDir, "amend.txt"), []byte("amend\n"), 0o644)
	runGitSSH(t, repoDir, sshCmd, "add", "amend.txt")
	runGitSSH(t, repoDir, sshCmd, "commit", "--amend", "-m", "amended")
	err := runGitSSHResult(repoDir, sshCmd, "push", "--force", "origin", "HEAD:refs/heads/main")
	if err == nil {
		t.Fatal("force push to main should be rejected")
	}

	// Delete main → should be rejected.
	err = runGitSSHResult(repoDir, sshCmd, "push", "origin", "--delete", "main")
	if err == nil {
		t.Fatal("delete main should be rejected")
	}
}

func TestSSHHostKeyPersistence(t *testing.T) {
	baseDir := t.TempDir()
	hostKeyPath := filepath.Join(baseDir, "host_key")
	store := git.NewStore(baseDir)
	keyStore := openTestKeyStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// First server start: generates host key.
	s1, err := sshd.NewServer(store, keyStore, hostKeyPath, logger)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1

	keyData1, _ := os.ReadFile(hostKeyPath)
	if len(keyData1) == 0 {
		t.Fatal("host key not generated")
	}

	// Second server start: loads existing key.
	s2, err := sshd.NewServer(store, keyStore, hostKeyPath, logger)
	if err != nil {
		t.Fatal(err)
	}
	_ = s2

	keyData2, _ := os.ReadFile(hostKeyPath)
	if !bytes.Equal(keyData1, keyData2) {
		t.Fatal("host key changed between restarts")
	}
}

// Test environment helpers.

type sshTestEnv struct {
	port    int
	keyFile string
}

func (e *sshTestEnv) cloneURL(ns, proj string) string {
	return fmt.Sprintf("ssh://git@127.0.0.1:%d/%s/%s.git", e.port, ns, proj)
}

func setupSSHEnv(t *testing.T) *sshTestEnv {
	t.Helper()
	baseDir := t.TempDir()
	store := git.NewStore(baseDir)
	if err := store.CreateRepo("ns", "proj"); err != nil {
		t.Fatal(err)
	}

	keyStore := openTestKeyStore(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Generate and register a test user key.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := gossh.NewPublicKey(pub)
	authorizedKey := string(gossh.MarshalAuthorizedKey(sshPub))
	if _, err := keyStore.Add("testuser@test.com", authorizedKey, "test"); err != nil {
		t.Fatal(err)
	}

	keyFile := filepath.Join(t.TempDir(), "test_key")
	writeSSHPrivateKey(t, keyFile, priv)

	hostKeyPath := filepath.Join(baseDir, "host_key")
	server, err := sshd.NewServer(store, keyStore, hostKeyPath, logger)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	ctx := t.Context()
	go server.Serve(ctx, ln)

	return &sshTestEnv{port: port, keyFile: keyFile}
}

func openTestKeyStore(t *testing.T) *sshkeys.Store {
	t.Helper()
	s, err := sshkeys.Open(filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func writeSSHPrivateKey(t *testing.T, path string, priv ed25519.PrivateKey) {
	t.Helper()
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(block)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func sshCommand(keyFile string, port int) string {
	return fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %d", keyFile, port)
}

func runGitSSH(t *testing.T, dir, sshCmd string, args ...string) {
	t.Helper()
	if err := runGitSSHResult(dir, sshCmd, args...); err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(args, " "), err)
	}
}

func runGitSSHResult(dir, sshCmd string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_SSH_COMMAND="+sshCmd,
	)
	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}
	return nil
}
