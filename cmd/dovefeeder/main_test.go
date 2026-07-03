package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgs_Full(t *testing.T) {
	opts, err := parseArgs([]string{"-n", "myns/myproj", "-a", "default_claude_coder", "implement feature X"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.nsProject != "myns/myproj" {
		t.Errorf("nsProject = %q", opts.nsProject)
	}
	if opts.agentName != "default_claude_coder" {
		t.Errorf("agentName = %q", opts.agentName)
	}
	if opts.prompt != "implement feature X" {
		t.Errorf("prompt = %q", opts.prompt)
	}
}

func TestParseArgs_LongFlags(t *testing.T) {
	opts, err := parseArgs([]string{
		"--namespace-project", "ns/proj",
		"--agent", "default_gemini_coder",
		"--keep-workdir",
		"--branch", "feat/test",
		"--verbose",
		"do something",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opts.nsProject != "ns/proj" {
		t.Errorf("nsProject = %q", opts.nsProject)
	}
	if opts.agentName != "default_gemini_coder" {
		t.Errorf("agentName = %q", opts.agentName)
	}
	if !opts.keepWorkdir {
		t.Error("keepWorkdir should be true")
	}
	if opts.branch != "feat/test" {
		t.Errorf("branch = %q", opts.branch)
	}
	if !opts.verbose {
		t.Error("verbose should be true")
	}
}

func TestParseArgs_MissingPrompt(t *testing.T) {
	_, err := parseArgs([]string{"-n", "ns/proj", "-a", "agent"})
	if err == nil {
		t.Fatal("expected error for missing prompt")
	}
	if !strings.Contains(err.Error(), "prompt") {
		t.Errorf("error = %q, want mentioning prompt", err.Error())
	}
}

func TestParseArgs_MissingNamespace(t *testing.T) {
	_, err := parseArgs([]string{"-a", "agent", "do stuff"})
	if err == nil {
		t.Fatal("expected error for missing namespace")
	}
}

func TestParseArgs_MissingAgent(t *testing.T) {
	_, err := parseArgs([]string{"-n", "ns/proj", "do stuff"})
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
}

func TestParseArgs_BadNamespaceFormat(t *testing.T) {
	_, err := parseArgs([]string{"-n", "noproject", "-a", "agent", "do stuff"})
	if err == nil {
		t.Fatal("expected error for bad namespace format")
	}
	if !strings.Contains(err.Error(), "namespace/project") {
		t.Errorf("error = %q, want mentioning format", err.Error())
	}
}

func TestParseArgs_Version(t *testing.T) {
	opts, err := parseArgs([]string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.showVersion {
		t.Error("showVersion should be true")
	}
}

func TestResolvePrompt_Direct(t *testing.T) {
	p, err := resolvePrompt("hello world")
	if err != nil {
		t.Fatal(err)
	}
	if p != "hello world" {
		t.Errorf("prompt = %q", p)
	}
}

func TestResolvePrompt_FromFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "prompt.md")
	os.WriteFile(f, []byte("implement feature Y"), 0o644)

	p, err := resolvePrompt("@" + f)
	if err != nil {
		t.Fatal(err)
	}
	if p != "implement feature Y" {
		t.Errorf("prompt = %q", p)
	}
}

func TestResolvePrompt_MissingFile(t *testing.T) {
	_, err := resolvePrompt("@/nonexistent/file.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestInjectTokenInURL(t *testing.T) {
	tests := []struct {
		url, token, want string
	}{
		{"https://gitcote.example.com/ns/proj.git", "tok123", "https://tok123@gitcote.example.com/ns/proj.git"},
		{"http://localhost:8080/ns/proj.git", "tok456", "http://tok456@localhost:8080/ns/proj.git"},
		{"ssh://git@example.com/repo", "tok", "ssh://git@example.com/repo"},
	}
	for _, tt := range tests {
		got := injectTokenInURL(tt.url, tt.token)
		if got != tt.want {
			t.Errorf("injectTokenInURL(%q, %q) = %q, want %q", tt.url, tt.token, got, tt.want)
		}
	}
}

func TestSubstituteVars(t *testing.T) {
	vars := map[string]string{
		"$NAMESPACE": "myns",
		"$PROJECT":   "myproj",
		"$PROMPT":    "do stuff",
	}
	result := substituteVars("Work on $NAMESPACE/$PROJECT: $PROMPT", vars)
	want := "Work on myns/myproj: do stuff"
	if result != want {
		t.Errorf("substituteVars = %q, want %q", result, want)
	}
}

func TestCopyDirContents(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	os.WriteFile(filepath.Join(src, "file.txt"), []byte("hello"), 0o644)
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "nested.txt"), []byte("world"), 0o644)

	if err := copyDirContents(src, dst); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dst, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("file.txt = %q", string(data))
	}

	data, err = os.ReadFile(filepath.Join(dst, "sub", "nested.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Errorf("sub/nested.txt = %q", string(data))
	}
}

func TestRun_Version(t *testing.T) {
	var out strings.Builder
	var errOut strings.Builder
	code := run([]string{"--version"}, &out, &errOut)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "dovefeeder") {
		t.Errorf("version output = %q, want containing dovefeeder", out.String())
	}
}
