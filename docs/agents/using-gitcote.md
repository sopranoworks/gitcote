# Using GitCote (Agent Guide)

Practical guidance for agents calling GitCote's MCP tools. For the full tool
list and permission levels, see `docs/agents/README.md`.

## Idiomatic patterns

1. **Create a PR via push options.** Push your branch with push options to create
   a PR in one step:
   ```
   git push origin feature-branch \
     -o pull_request.create \
     -o pull_request.target=main \
     -o "pull_request.title=Add widget support"
   ```
   If the branch is already pushed, call `create_pull_request` via MCP instead.

2. **Read a PR diff and files.** Use `get_pull_request_diff` for the unified diff
   and `get_pull_request_files` for the changed-file list. Use `get_pull_request`
   to check mergeable status and conflict details before approving.

3. **Approve or reject a PR.** Call `approve_pull_request` to approve (fails if
   there are conflicts or the PR is not open). Call `reject_pull_request` with a
   `reason` to reject. Both accept optional `review_files` paths for traceability.

4. **Work with Git tokens.** Tokens are issued by the operator via
   `issue_git_token`. A token carries:
   - **scope** — `r` (read-only: clone/fetch) or `rw` (read-write: push).
   - **allowed_branches** — optional prefix restrictions (e.g. `["feature/"]`).
   - **TTL** — the token expires after this duration.

   Use the token as HTTP basic-auth password when cloning or pushing:
   ```
   git clone http://git-token:<token>@host:port/namespace/project.git
   ```

5. **Browse repository files via MCP.** `list_files` returns entries at a path
   (default: root). `read_file` returns file content. Both accept an optional
   `ref` (branch name, tag, or commit SHA; default: HEAD).

6. **Search commit history.** `get_log` returns commit history, optionally
   filtered by `path` and bounded by `limit` (default: 20, max: 100).
   `get_commit` returns details for a single commit by SHA.

7. **Attach context files to a PR.** Use `order_files` (what the coder was told
   to implement) and `result_files` (what the coder produced) when creating a PR.
   Reviewers read these for context.

## Pitfalls

1. **Do not push to main directly.** Branch protection rejects pushes to the
   default branch. Always push to a feature branch and create a PR.

2. **Do not assume merge will succeed.** Check `get_pull_request` for the
   `mergeable` field and `conflicts` array before approving. If conflicts exist,
   the approval call will fail.

3. **Do not hold tokens longer than needed.** Tokens have a TTL. Clone, do your
   work, push, and let the token expire. Do not cache tokens across sessions.

4. **Do not push to branches outside your allowed prefixes.** If the token has
   `allowed_branches` set (e.g. `["feature/"]`), pushes to branches that don't
   match any prefix are rejected. Approvals targeting branches outside the
   allowed set are also rejected.

5. **Do not call admin tools with a read/write token.** Tools like
   `list_projects`, `push_to_seed`, `retry_pr_agent` require admin-level
   authorization. A scoped Git token will not see or be able to call these tools.

6. **Do not expect to merge or close PRs.** Merge and close are operator-only
   actions (admin level, via WebUI or admin MCP connection). Agents review,
   approve, or reject.

7. **Understand interrupted state.** If an agent crashes or times out mid-review,
   the PR enters `interrupted` state. Only an admin can `retry_pr_agent` or
   `dismiss_pr_interrupt` to recover.

8. **Do not confuse namespace with project.** Every MCP tool takes `namespace`
   and `project_name` as separate parameters. The repository path is
   `<namespace>/<project_name>`. Most tokens are scoped to one namespace/project.

## When to ask the user vs. proceed

- **Proceed:** reads (file listing, diff, log, PR details), pushes to feature
  branches, creating PRs, approving/rejecting based on review.
- **Ask first:** anything that changes `main` or the default branch, any admin
  operation (seed push/pull, retry agent, dismiss interrupt), any action that
  would discard another agent's work.

## Sources

- `cmd/gitcote/prwiring.go` (PR creation via push options, approval logic).
- `cmd/gitcote/tokenwiring.go` (token issuance, branch restrictions).
- `internal/git/handler.go` (branch protection enforcement).
