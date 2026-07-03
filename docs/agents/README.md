# GitCote for Agents

GitCote is an agent-oriented Git host — a dovecote where coding agents come and
go, converging work into one place. Agents push to GitCote over Smart HTTP; an
operator reviews and approves pull requests; GitCote dispatches approved changes
onward to seed (master) repositories via SSH. All interaction is via MCP tools
(Streamable HTTP at `/mcp`) or Git Smart HTTP — there is no REST API.

## Where to look

| You need | Read |
|----------|------|
| Practical patterns and pitfalls for calling MCP tools | `docs/agents/using-gitcote.md` |
| The PR lifecycle step-by-step | `docs/agents/pr-workflow.md` |
| Agent template reference (agent.yaml, variables, hooks) | `docs/agents/agent-templates.md` |
| Running & configuring GitCote | `docs/OPERATIONS.md` |
| dovefeeder CLI (spawn coding agents) | `docs/dovefeeder.md` |

## Three things to know up front

1. **Auth is token-scoped.** Git tokens issued by `issue_git_token` carry a scope
   (`r` or `rw`), optional branch restrictions, and a TTL. MCP tool visibility is
   filtered by the connection's authorization level (read / write / admin).
2. **PRs are created via push options.** Push with
   `-o pull_request.create -o pull_request.target=main` to create a PR on push —
   or call `create_pull_request` via MCP after pushing. Pushing to `main` directly
   is blocked by branch protection.
3. **Merge is operator-only.** Agents can review, approve, and reject PRs. Only an
   operator (admin) can merge or close. The merge happens server-side — no local
   merge needed.

## MCP tools (17 tools)

| Tool | Level | Purpose |
|------|-------|---------|
| `get_server_info` | read | Server name, version, public URL |
| `list_projects` | admin | List all projects (optionally scoped to a namespace) |
| `list_files` | read | List files/directories at a path in a repository |
| `read_file` | read | Read file contents (supports ref/branch/SHA) |
| `list_branches` | read | List branches in a repository |
| `get_log` | read | Commit history (optionally filtered by path, limited) |
| `get_commit` | read | Details of a single commit by SHA |
| `create_pull_request` | write | Create a PR from source to target branch |
| `list_pull_requests` | read | List PRs, optionally filtered by state |
| `get_pull_request` | read | Get a single PR with mergeable status and conflicts |
| `get_pull_request_diff` | read | Unified diff for a PR |
| `get_pull_request_files` | read | Changed file list for a PR |
| `approve_pull_request` | write | Approve an open PR |
| `reject_pull_request` | write | Reject an open PR with a reason |
| `retry_pr_agent` | admin | Re-spawn an agent on an interrupted PR |
| `dismiss_pr_interrupt` | admin | Clear interrupted state without re-spawning |
| `push_to_seed` | admin | Push a branch to the seed repository |
| `pull_from_seed` | admin | Pull from the seed repository |
| `get_seed_status` | read | Seed sync status for a project |
| `issue_git_token` | internal | Issue a scoped Git token (never advertised to agents) |

## Sources

- `cmd/gitcote/toolfilter.go` (tool permission levels).
- `cmd/gitcote/repowiring.go`, `cmd/gitcote/prwiring.go`,
  `cmd/gitcote/tokenwiring.go`, `cmd/gitcote/seedwiring.go` (tool definitions).
