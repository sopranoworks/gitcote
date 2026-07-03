# PR Workflow (Agent Guide)

Step-by-step guide to the pull request lifecycle from an agent's perspective.

## 1. Creating a PR

### Via push options (preferred)

Push a feature branch with push options to create a PR in a single operation:

```
git push origin feature-branch \
  -o pull_request.create \
  -o pull_request.target=main \
  -o "pull_request.title=Implement feature X"
```

Available push options:

| Option | Required | Description |
|--------|----------|-------------|
| `pull_request.create` | yes | Triggers PR creation |
| `pull_request.target` | no | Target branch (default: HEAD / default branch) |
| `pull_request.title` | no | PR title (default: source branch name) |
| `pull_request.order_files` | no | Comma-separated paths to instruction files |
| `pull_request.result_files` | no | Comma-separated paths to result/report files |

If a PR already exists for the same source-target branch pair, the push updates
the existing PR (new source commit, recomputed mergeable status) instead of
creating a new one. An existing approval is invalidated on update.

### Via MCP tool

If the branch is already pushed, call `create_pull_request` with:
- `namespace`, `project_name` (required)
- `source_branch`, `title` (required)
- `target_branch` (optional, defaults to default branch)
- `description`, `order_files`, `result_files` (optional)

### Branch naming

No enforced convention. Common patterns: `feature/<slug>`, `fix/<slug>`,
`agent/<task-id>`. The branch must not match an existing branch when the token
has `allowed_branches` restrictions — token issuance rejects prefixes that
match existing branches.

## 2. Review flow

When a PR is created, GitCote can auto-spawn a reviewer agent (if configured in
the project's PR event settings). The reviewer agent:

1. Receives the PR ID, source/target branches, and any order/result file paths.
2. Calls `get_pull_request_diff` to read the unified diff.
3. Calls `get_pull_request_files` to list changed files.
4. Optionally reads order files (instruction context) and result files
   (agent-produced context) referenced by the PR.
5. Reads individual files via `read_file` if deeper inspection is needed.
6. Calls `approve_pull_request` or `reject_pull_request` when done.

### What the reviewer sees

- `get_pull_request` — PR metadata, state, mergeable status, conflict details.
- `get_pull_request_diff` — full unified diff (source vs. target).
- `get_pull_request_files` — list of changed files with change type.

## 3. Approval

Call `approve_pull_request` with `namespace`, `project_name`, and `number`.
Optionally attach `review_files` paths for traceability.

**Preconditions (enforced server-side):**
- PR must be in `open` state.
- PR must not have merge conflicts (`mergeable` != `conflict`).
- If the token has `allowed_branches`, the PR's target branch must match.

On approval, the PR state transitions to `approved`. If an auto-merge agent is
configured, GitCote spawns it.

## 4. Rejection

Call `reject_pull_request` with `namespace`, `project_name`, `number`, and an
optional `reason`. Optionally attach `review_files`.

On rejection:
- PR state transitions to `rejected`.
- If an `on_rejected` event hook is configured, GitCote spawns a coder agent to
  address the rejection (the coder receives the rejection reason).
- The coder fixes the issue, pushes to the same branch, and the push updates
  the existing PR back to `open` state (re-triggering review).

## 5. Merge flow

Merge is **operator-only** (admin level, via WebUI). Agents cannot merge.

When the operator merges:
1. GitCote computes the merge tree. If conflicts exist, the PR moves to
   `merge_conflict` state and a merger agent is spawned (if configured).
2. If clean, a merge commit is created, the target branch is updated, and the
   source branch is deleted.
3. If seed sync is configured with `push_mode: on_merge`, GitCote pushes to the
   seed repository automatically.

## 6. Conflict resolution

If the merge reveals conflicts:
- PR state transitions to `merge_conflict`.
- GitCote creates a temporary clone with both the seed and GitCote remotes.
- A merger agent is spawned (if configured) with the clone path and conflict
  file list.
- The merger resolves conflicts in the temp clone, commits, and pushes back to
  GitCote. This updates the PR, making it mergeable again.

## 7. Interrupted state

If an agent crashes, times out (hard timeout), or exits non-zero:
- The PR transitions to `interrupted` state.
- The previous state is preserved (`interrupted_previous_status`).
- Only an admin can recover via `retry_pr_agent` (re-spawn) or
  `dismiss_pr_interrupt` (restore previous state for manual handling).

## PR state machine

```
          push
           |
           v
        [open] ──approve──> [approved] ──merge──> [merged]
           |                    |                     |
           |                    |              (seed push if
           |                    |               on_merge)
         reject             operator
           |                 reject
           v                    |
       [rejected]  <────────────┘
           |
      (on_rejected
       coder push)
           |
           v
        [open]  (cycle continues)

    Any agent state can transition to [interrupted] on agent failure.
    [approved] ──merge with conflicts──> [merge_conflict]
    [merge_conflict] ──merger resolves──> [approved] (retry merge)
    Admin can [close] any non-terminal PR.
```

## Sources

- `cmd/gitcote/prwiring.go` (PR creation, approval, rejection, merge, state
  transitions, push option parsing).
- `internal/git/pushopts.go` (push option extraction from Git protocol).
- `cmd/gitcote/main.go` (event hook dispatch).
