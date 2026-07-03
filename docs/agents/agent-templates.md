# Agent Templates

GitCote spawns coding agents using configurable templates. Each template defines
a role, a shell command, a prompt, and an optional environment directory.

## Directory structure

```
<agents_root>/
  my_reviewer/
    agent.yaml                  # required — role, command, prompt
    environment_default/        # optional — files copied into the agent workdir
      CLAUDE.md
      .mcp.json
      prepare.sh                # optional — runs after copy, before agent
```

Builtin templates are embedded in the GitCote binary under
`pkg/agent/agents/`. User templates in the configured `agents_root` directory
override builtins with the same directory name.

## agent.yaml

```yaml
agent:
  role: reviewer              # required: "reviewer", "coder", or "merger"
  display_name: "My Reviewer" # optional (defaults to directory name)
  command: 'claude -p "$PROMPT"'  # required: shell command to spawn
  prompt: |                    # required: prompt template (variables substituted)
    Review PR $PR_ID ($SOURCE_BRANCH -> $TARGET_BRANCH).
    Call approve_pull_request or reject_pull_request when done.
```

### Fields

| Field | Required | Description |
|-------|----------|-------------|
| `role` | yes | Agent role: `reviewer`, `coder`, or `merger` |
| `display_name` | no | Human-readable name shown in the UI |
| `command` | yes | Shell command to execute (run via `sh -c`) |
| `prompt` | yes | Prompt template with variable substitution |

## Variable substitution

Variables in `command`, `prompt`, and all text files in `environment_default/`
are substituted before use. Binary files are copied as-is.

| Variable | Description |
|----------|-------------|
| `$PR_ID` | PR identifier string |
| `$PR_NUMBER` | PR number (integer) |
| `$NAMESPACE` | Repository namespace |
| `$PROJECT` | Repository project name |
| `$SOURCE_BRANCH` | PR source branch |
| `$TARGET_BRANCH` | PR target branch |
| `$DIRECTIVE` | Coder directive (instruction text) |
| `$REPORT` | Report content |
| `$PROMPT` | Resolved prompt (after substitution of other variables) |
| `$TEMP_CLONE_DIR` | Path to temporary clone (merger) |
| `$CONFLICT_FILES` | Comma-separated conflicting file paths (merger) |
| `$GIT_URL` | Git clone URL for the repository |
| `$ORDER_FILES` | Paths to order/instruction files |
| `$RESULT_FILES` | Paths to result/report files |
| `$REVIEW_FILES` | Paths to review files |
| `$REJECTION_REASON` | Rejection reason text (coder, on re-work) |
| `$TOKEN` | Scoped Git access token |
| `$WORK_DIR` | Agent working directory path |

Variables are also exported as environment variables (without the `$` prefix)
for use in `prepare.sh` and the agent command.

## environment_default/

Files in this directory are copied into the agent's temporary working directory
before the agent runs. Text files undergo variable substitution; binary files
are copied verbatim.

Common uses:
- `CLAUDE.md` / `GEMINI.md` — agent instruction files.
- `.mcp.json` — MCP server configuration for the agent.
- `prepare.sh` — setup script (runs after copy, before agent launch).

### prepare.sh

If `prepare.sh` exists in the working directory after the environment copy, it
runs via `sh` before the agent command. It receives all variables as environment
variables. Use it for setup that can't be done with static files (e.g. cloning,
installing dependencies).

## Builtin templates (8)

| Directory name | Role | Agent | Notes |
|----------------|------|-------|-------|
| `default_claude_coder` | coder | Claude | Passes `$PROMPT` through |
| `default_claude_reviewer` | reviewer | Claude | Reviews diff, calls approve/reject |
| `default_claude_merger` | merger | Claude | Clones via `$GIT_URL`, resolves conflicts |
| `default_gemini_coder` | coder | Gemini CLI | Passes `$PROMPT` through |
| `default_gemini_reviewer` | reviewer | Gemini CLI | Reviews diff, calls approve/reject |
| `default_gemini_merger` | merger | Gemini CLI | Clones via `$GIT_URL`, resolves conflicts |
| `default_codex_reviewer` | reviewer | Codex | Reviews diff, calls approve/reject |
| `default_codex_merger` | merger | Codex | Uses `$TEMP_CLONE_DIR` for conflicts |

## Creating custom templates

1. Create a directory under your configured `agents_root`.
2. Add an `agent.yaml` with `role`, `command`, and `prompt`.
3. Optionally add `environment_default/` with instruction files.
4. To override a builtin, name your directory the same (e.g.
   `default_claude_reviewer`) — the user config takes precedence.

Custom templates appear in the agent list (WebUI) and can be selected for PR
event hooks.

## Sources

- `pkg/agent/config.go` (config scanning, YAML parsing).
- `pkg/agent/spawn.go` (workdir preparation, variable substitution, execution).
- `pkg/agent/defaults.go` (builtin template embedding and loading).
