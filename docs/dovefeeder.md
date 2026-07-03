# dovefeeder

CLI tool for spawning coding agents against GitCote repositories.

## Overview

dovefeeder connects to a GitCote MCP server, obtains a scoped Git token, clones
the target repository into a temporary working directory, sets up the agent
environment, and spawns a coding agent with a prompt. The agent does its work
(commit, push, create PRs), and dovefeeder cleans up when it exits.

The name follows the dovecote metaphor: GitCote is the dovecote (pigeon house),
and dovefeeder launches the carrier pigeons that fly off to do coding work.

**Use cases:**

- **Dogfooding** — run coding agents against GitCote during development.
- **Ad-hoc tasks** — one-off agent coding jobs from the command line.
- **CI integration** — script agent-driven changes in a pipeline.
- **Bridge tool** — fills the gap until Rohrpost (the full agent dispatch
  system) is implemented.

## Installation

### From source (recommended)

```sh
go build -o dovefeeder ./cmd/dovefeeder
```

### go install

```sh
go install github.com/sopranoworks/gitcote/cmd/dovefeeder@latest
```

### Version check

```sh
dovefeeder --version
```

## Configuration

dovefeeder needs to know where your GitCote MCP server is and how to
authenticate. This is provided via a YAML config file, environment variables, or
both.

### Config file

dovefeeder searches for a config file in this order:

1. Path given by `--config` / `-c`
2. `dovefeeder.yaml` in the current directory
3. `~/.config/dovefeeder/config.yaml`

### Config file format

```yaml
gitcote:
  mcp_url: "https://gitcote.example.com/mcp"   # MCP endpoint (Streamable HTTP)
  oauth_token: "tok_..."                        # OAuth access token
```

### Environment variable overrides

| Variable | Overrides | Description |
|----------|-----------|-------------|
| `GITCOTE_MCP_URL` | `gitcote.mcp_url` | MCP server URL |
| `GITCOTE_OAUTH_TOKEN` | `gitcote.oauth_token` | OAuth Bearer token |

**Precedence:** environment variable > config file value.

Environment-only usage (no config file needed):

```sh
export GITCOTE_MCP_URL="https://gitcote.example.com/mcp"
export GITCOTE_OAUTH_TOKEN="tok_..."
dovefeeder -n myns/myproj -a default_claude_coder "Fix the login bug"
```

## Usage

```
dovefeeder [options] <prompt>
```

### Arguments

| Argument | Description |
|----------|-------------|
| `prompt` | The task prompt for the coding agent (required, positional). If it starts with `@`, the rest is read as a file path (e.g. `@prompt.md`). |

### Options

| Short | Long | Argument | Description |
|-------|------|----------|-------------|
| `-n` | `--namespace-project` | `<ns/proj>` | Target repository in `namespace/project` format. **Required.** |
| `-a` | `--agent` | `<name>` | Agent template name (e.g. `default_claude_coder`). **Required.** |
| `-k` | `--keep-workdir` | — | Keep the working directory after completion (default: delete on success). |
| `-c` | `--config` | `<path>` | Config file path. |
| `-b` | `--branch` | `<name>` | Branch to work on. Default: use the repository's default branch. |
| `-v` | `--verbose` | — | Verbose output (debug-level logging). |
| | `--version` | — | Print version and exit. |

### Examples

Basic usage:

```sh
dovefeeder -n myns/myproject -a default_claude_coder "Implement feature X"
```

Work on a specific branch:

```sh
dovefeeder -n myns/myproject -a default_claude_coder -b feat/new-feature "Add login page"
```

Prompt from a file:

```sh
dovefeeder -n myns/myproject -a default_claude_coder @prompt.md
```

Keep the working directory for inspection:

```sh
dovefeeder -n myns/myproject -a default_gemini_coder -k "Refactor the auth module"
```

Custom config and verbose output:

```sh
dovefeeder -c ./my-config.yaml -v -n myns/myproject -a default_claude_coder "Fix bug Y"
```

## Agent templates

dovefeeder uses the same agent template system as GitCote's server-side agent
spawn. Templates are loaded from `go:embed` builtins compiled into the binary.

### Builtin templates

| Name | Role | Agent CLI | Description |
|------|------|-----------|-------------|
| `default_claude_coder` | coder | `claude` | Claude Code in bypass-permissions mode |
| `default_claude_reviewer` | reviewer | `claude` | Claude Code for PR review |
| `default_claude_merger` | merger | `claude` | Claude Code for merge conflict resolution |
| `default_gemini_coder` | coder | `gemini` | Gemini CLI in yolo/skip-trust mode |
| `default_gemini_reviewer` | reviewer | `gemini` | Gemini CLI for PR review |
| `default_gemini_merger` | merger | `gemini` | Gemini CLI for merge conflict resolution |
| `default_codex_reviewer` | reviewer | `codex` | Codex CLI for PR review |
| `default_codex_merger` | merger | `codex` | Codex CLI for merge conflict resolution |

For dovefeeder, the **coder** templates are the primary choice.

### Custom templates

Custom agent templates follow the same directory structure used by GitCote's
server-side agent configs. Each template is a directory containing:

```
my_custom_agent/
  agent.yaml                  # agent definition (required)
  environment_default/        # files copied into the workdir (optional)
    CLAUDE.md                 # or GEMINI.md, etc.
```

#### agent.yaml format

```yaml
agent:
  role: coder                                    # role identifier
  display_name: "My Custom Coder"                # human-readable name
  command: 'claude -p "$PROMPT"'                 # shell command to execute
  prompt: |                                      # prompt template
    $PROMPT
```

#### Variable substitution

These variables are available in `agent.yaml` fields and in files under
`environment_default/`:

| Variable | Value |
|----------|-------|
| `$NAMESPACE` | Target namespace |
| `$PROJECT` | Target project name |
| `$GIT_URL` | Clone URL with embedded token |
| `$TOKEN` | Git access token |
| `$WORK_DIR` | Working directory path |
| `$PROMPT` | The user's prompt |

## Execution flow

dovefeeder runs through these steps in order:

### 1. Load configuration

Read the config file and apply environment variable overrides. Resolve the agent
template from the builtin `go:embed` configs.

### 2. Connect to GitCote MCP

Open a Streamable HTTP connection to the MCP server using the configured OAuth
token. Verify the connection by calling `list_projects`.

### 3. Issue Git token

Call `issue_git_token` via MCP to obtain a scoped, time-limited Git access token
for the target namespace/project. The token has read-write scope and a 2-hour
TTL. If `--branch` is specified, the token includes branch prefix restrictions.

### 4. Clone the repository

Create a temporary directory (`/tmp/dovefeeder-<ns>-<proj>-<random>/`) and clone
the repository using the Git token. If `--branch` is specified, check out (or
create) that branch.

### 5. Set up agent environment

Copy files from the agent template's `environment_default/` into the cloned
repository, with variable substitution applied. Write `.mcp.json` and
`.gemini/settings.json` so the spawned agent has its own MCP connection to
GitCote.

### 6. Spawn the agent

Execute the resolved command from `agent.yaml` via `sh -c`. The agent's stdout
and stderr are streamed to the terminal. The working directory is the cloned
repository.

### 7. Monitor and cleanup

Wait for the agent process to exit.

- **Exit 0 (success):** Print success message. If `--keep-workdir` is set, print
  the working directory path; otherwise delete it.
- **Non-zero exit (failure):** Print the exit code. The working directory is
  **always preserved** on failure for debugging, regardless of `--keep-workdir`.

## Security

### Token handling

- **OAuth token** — stored in the config file or passed via `GITCOTE_OAUTH_TOKEN`.
  Transmitted as a Bearer token in MCP requests.
- **Git token** — issued by GitCote with a scoped lifetime (2h). Used for clone
  and embedded in `.mcp.json` for the spawned agent. The token is **never printed
  in full** in error messages — only the first 8 characters are shown.
- **Agent isolation** — each agent run gets its own temporary directory and its
  own scoped token. The working directory is deleted on success.

### Recommendations

- Store OAuth tokens in environment variables rather than config files in shared
  environments.
- Use `--branch` to restrict the Git token to a specific branch prefix.
- Review the agent's changes before merging (the agent creates PRs via MCP, not
  direct pushes to protected branches).

## Reference

- **GitCote operations** — server setup, configuration, OAuth:
  [`OPERATIONS.md`](OPERATIONS.md)
- **Agent template system** — the `pkg/agent/` package provides config loading,
  environment preparation, variable substitution, and agent execution. It is
  shared between dovefeeder and GitCote's server-side agent spawn.
