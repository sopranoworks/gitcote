# GitYard Operations

How to build, configure, and run GitYard.

## Building

### Prerequisites

- **Go >= 1.26.2** (declared in `go.mod`)
- **Node.js** — only if modifying the frontend source in `web/`

### Build steps

```sh
go build -o gityard ./cmd/gityard
```

The frontend is **pre-built and embedded** in the binary (`cmd/gityard/dist/`,
`cmd/gityard/embed.go`). A plain `go build` produces a working binary with the
WebUI included — no separate frontend build step is needed.

If you modify the frontend source in `web/`:

```sh
cd web && npm install && npm run build
cd ..
go build -o gityard ./cmd/gityard
```

`npm run build` compiles the TypeScript/React source into `cmd/gityard/dist/`,
which `go build` then embeds.

### Version check

```sh
./gityard --version
```

Prints the version and exits without starting the server.

## Running

```sh
./gityard --config gityard.yaml
```

The `--config` flag defaults to `gityard.yaml`. On startup GitYard creates
`storage.base_dir` if absent and starts up to three listeners (web, MCP-plain,
MCP-OAuth).

## Configuration reference

Configuration is a YAML file. A fully annotated example is
`gityard.example.yaml` — the canonical reference for every key and its default.
The config has **three top-level sections**: `server`, `storage`, and `identity`.

The required keys are `server.http.listen`, `storage.base_dir`, and **at least
one MCP transport** (`server.mcp.plain.listen` and/or
`server.mcp.oauth.listen`); every other key is optional and falls back to a
built-in default.

### Strict config decoding

The config is decoded **strictly**: an unknown key (a typo like `storagee:`) or
a known key in the wrong block is a **hard load error** that names the offending
key — the server does not start. Every valid config (including
`gityard.example.yaml`) is unaffected.

### `server` — listeners, auth, logging

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `server.http.listen` | string | **required** | Web UI, WebSocket (`/ws/ui`), and Git hosting (`/git/`) address. |
| `server.http.external_url` | string | `""` | Public URL reported by `get_server_info`. |
| `server.mcp.plain.listen` | string | ** | Plain (internal) MCP transport address. Clients connect at `/mcp`. |
| `server.mcp.plain.external_url` | string | `""` | Public URL for the plain transport. |
| `server.mcp.plain.bearer_auth` | bool | `false` | Off = unauthenticated (loopback/internal use only). On = require `Authorization: Bearer <token>` validated against `server.auth.tokens` (**must** be behind TLS). |
| `server.mcp.oauth.listen` | string | ** | OAuth (external) MCP transport. Its **presence** enables the built-in OAuth authorization server — there is no separate flag. |
| `server.mcp.oauth.external_url` | string | `""` | Public origin for OAuth discovery documents. |
| `server.mcp.oauth.registration_mode` | string | `"cimd"` | Client registration posture: `cimd` (default), `dcr` (RFC 7591), or `confidential` (pre-issued Client ID + Secret). |
| `server.auth.enabled` | bool | `false` | Static-bearer auth for Web routes (the gate the Web UI was built on). Does **not** gate MCP — MCP auth is per-transport. |
| `server.auth.tokens` | []string | `[]` | Accepted bearer tokens (also the API-Token set for `bearer_auth`). |
| `server.auth.allowed_origins` | []string | `[]` | WebSocket `Origin` allowlist for `/ws/ui`. Empty = allow all when auth is off; reject empty when auth is on. |
| `server.auth.users.session_ttl` | duration | `"720h"` | Login session lifetime. |
| `server.auth.users.allow_first_run_admin` | bool | `true` | Allow bootstrapping the first admin account on an empty user store. |
| `server.auth.users.totp_encryption_key` | string | `""` | Base64 32-byte key for TOTP secret encryption. When empty, a key is generated and persisted at `<base_dir>/userstore.key` on first run. |
| `server.log.level` | string | `"info"` | Minimum log severity: `error` \| `warn` \| `info` \| `debug`. |
| `server.log.format` | string | `"text"` | Log format: `text` (key=value) \| `json`. |
| `server.log.output` | string | `"stderr"` | Where logs go: `stderr` (default, captured by service manager) \| `file` (bounded, self-rotating). |
| `server.log.file.path` | string | — | **Required** when `output: file`. Parent directories are created; an unopenable path fails startup. |
| `server.log.file.max_size_mb` | int | `100` | Rotate the active file past this size. |
| `server.log.file.max_backups` | int | `7` | Rotated backups retained. |
| `server.log.file.max_age_days` | int | `30` | Days rotated backups are retained. |
| `server.log.file.compress` | bool | `false` | Gzip rotated backups. |
| `server.log.file.rotate_daily` | bool | `true` | Rotate at least once per day even without size pressure. |
| `server.debug.dump_http` | bool | `false` | Verbatim HTTP request/response dump on **all** surfaces — method, path, headers, full body, status. Unredacted (includes tokens in clear). Enable for a debug session, read the log, turn it off. |

** At least one of `server.mcp.plain.listen` / `server.mcp.oauth.listen` must
be set; neither is a startup error.

### `storage` — data root

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `storage.base_dir` | string | **required** | Root directory for Git repositories (`<base_dir>/<namespace>/<project>`). A leading `~/` is expanded to the user's home directory. Created on startup if absent. |

### `identity` — operator principal

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `identity.user.name` | string | `""` | Operator name (bound onto issued OAuth tokens). |
| `identity.user.email` | string | `""` | Operator email. |

## First run

1. Build and start:

   ```sh
   go build -o gityard ./cmd/gityard
   cp gityard.example.yaml gityard.yaml   # edit as needed
   ./gityard --config gityard.yaml
   ```

2. Open `http://localhost:8080` in a browser.

3. The **first-run wizard** appears — create the administrator account (email,
   display name, password). This step is available only while the user store is
   empty and `allow_first_run_admin` is true (the default).

4. After setup, the **Settings** page loads, showing the Namespace / project
   management screen. The WebSocket connects and all management functions are
   available (user management, namespace/project CRUD, SSH keys, seed config).

## Connecting MCP clients

GitYard serves MCP over **Streamable HTTP** at the `/mcp` path of an MCP
transport's listen address.

**Claude Code** (CLI):

```sh
claude mcp add --transport http gityard http://localhost:8081/mcp
```

A non-CLI client (e.g. Claude Desktop) connects through the `mcp-remote`
bridge — add an `mcpServers` entry that runs
`npx mcp-remote http://localhost:8081/mcp`.

The `http://localhost:8081/mcp` shown matches `gityard.example.yaml`'s default
`server.mcp.plain.listen` (`:8081`); substitute your own address.

## Git hosting

Repositories are hosted at `/git/<namespace>/<project>.git/` over Smart HTTP
(pure Go, go-git v6 — no external `git` binary required).

### Clone and push

```sh
# Clone
git clone http://localhost:8080/git/default/myproject.git

# Push
git push origin main
```

Authentication uses **HTTP Basic Auth** with any valid token from
`server.auth.tokens` as the password (the username field is ignored — any
value works). When auth is disabled (`server.auth.enabled: false`), push is
unauthenticated.

### Creating repositories

- **MCP**: use the `create_project` tool (optionally with a `clone_url` to
  seed from an existing repository).
- **WebUI**: Settings → Namespace / project management → "+ Add project".

### Pull requests via push options

Create a PR by pushing with git push options:

```sh
git push \
  -o pull_request.create \
  -o pull_request.target=main \
  -o pull_request.title="My PR" \
  origin feature-branch
```

PRs are then managed via MCP tools: `list_pull_requests`,
`get_pull_request`, `approve_pull_request`, `merge_pull_request`,
`get_pull_request_diff`, `get_pull_request_files`.

## SSH keys and seed sync

GitYard can push repositories to a **seed** (upstream) repository over SSH.
SSH keys are managed at the **namespace** level (one key serves all projects
in the namespace), stored encrypted in `<base_dir>/keys.db`.

### Creating SSH keys

1. Open the WebUI → Settings → Namespace / project management.
2. In a namespace block, the **SSH Keys** section appears at the bottom
   (visible to super-users).
3. Enter a key name and click **Generate Key**.
4. Copy the displayed public key and add it as a deploy key on your seed host
   (e.g. GitHub → repository → Settings → Deploy keys → Add).

Keys are encrypted at rest with AES-256-GCM using an argon2id-derived master
key from the super-user's password. The master key is held **in memory only**
and never written to disk.

### Configuring a seed repository

In the project's row within the management screen, the **Seed** section shows:

1. **Seed URL** — e.g. `git@github.com:org/repo.git`
2. **SSH Key** — dropdown populated from the namespace's keys
3. **Push mode** — Disabled / On merge / Periodic
4. **Push interval** — shown when periodic (e.g. `6h`)

Click **Save** to persist, then **Test Connection** to verify SSH connectivity.

### Push modes

| Mode | Behaviour |
|------|-----------|
| **disabled** (default) | No automatic push. Use the **Push Now** button for manual push. |
| **on-merge** | Push automatically after each PR merge. |
| **periodic** | Push on a schedule (configurable interval, checked every 5 minutes by the background scheduler). |

### Vault unlock after restart

After a server restart, the SSH key vault is **locked** — the in-memory master
key is cleared. All seed push is paused until a super-user unlocks it:

1. The **Resume banner** appears at the top of the WebUI.
2. Enter your super-user email and password.
3. Click **Resume** — the vault unlocks and seed push resumes.

This is a deliberate safety step: the operator confirms healthy state before
resuming push to external seeds. GitYard is a private staging yard, not an
unattended public service.

### Sync status

Each project shows a **sync status badge** in the management screen:

| State | Indicator | Meaning |
|-------|-----------|---------|
| active | green | Last push succeeded (shows timestamp) |
| pending | yellow | Awaiting vault resume after restart |
| disabled | gray | Push mode is disabled (manual only) |
| error | red | Last push failed (hover for details) |

## Enabling OAuth (MCP)

GitYard includes a built-in **OAuth 2.1 authorization server**. It is active
when `server.mcp.oauth.listen` is set — its presence is the switch.

```yaml
server:
  mcp:
    oauth:
      listen: ":8082"
      external_url: "https://your-public-origin"
      registration_mode: "cimd"
```

### Prerequisites

- A **TLS-terminating reverse proxy** in front of GitYard (OAuth requires
  HTTPS; GitYard terminates no TLS).
- `server.mcp.oauth.external_url` set to the public origin.

### Registration modes

| Mode | Description |
|------|-------------|
| `cimd` (default) | Client ID Metadata Document. The `client_id` is an HTTPS URL pointing to client metadata. Used by Claude. |
| `dcr` | RFC 7591 Dynamic Client Registration. Advertises a `/register` endpoint. |
| `confidential` | Pre-issued Client ID + Secret. The operator issues credentials from the WebUI; the client authenticates at `/token` with the secret + PKCE. |

The OAuth transport speaks **purely OAuth** — static `Authorization: Bearer`
tokens are not accepted on it. The separate plain transport carries the
static-bearer / unauthenticated path.

## MCP tools reference

GitYard exposes **16 MCP tools** over Streamable HTTP at `/mcp`:

### Server

| Tool | Description |
|------|-------------|
| `get_server_info` | Server name, version, and public URL. |

### Projects

| Tool | Parameters | Description |
|------|-----------|-------------|
| `create_project` | `namespace`, `project_name`, `clone_url`? | Create a new Git repository (optionally clone from a URL). |
| `list_projects` | `namespace`? | List all repositories, optionally scoped to a namespace. |

### Repository browsing

| Tool | Parameters | Description |
|------|-----------|-------------|
| `list_files` | `namespace`, `project_name`, `path`?, `ref`? | List files and directories at a path. |
| `read_file` | `namespace`, `project_name`, `path`, `ref`? | Read file contents. |
| `list_branches` | `namespace`, `project_name` | List branches in a repository. |
| `get_log` | `namespace`, `project_name`, `ref`?, `path`?, `limit`? | View commit history. |
| `get_commit` | `namespace`, `project_name`, `sha` | Get details of a specific commit. |

### Pull requests

| Tool | Parameters | Description |
|------|-----------|-------------|
| `list_pull_requests` | `namespace`, `project_name`, `state`? | List PRs (open / merged / all). |
| `get_pull_request` | `namespace`, `project_name`, `id` | PR details. |
| `approve_pull_request` | `namespace`, `project_name`, `id` | Approve a PR. |
| `merge_pull_request` | `namespace`, `project_name`, `id` | Merge an approved PR. |
| `get_pull_request_diff` | `namespace`, `project_name`, `id` | View the diff of a PR. |
| `get_pull_request_files` | `namespace`, `project_name`, `id` | List files changed in a PR. |

### Seed sync

| Tool | Parameters | Description |
|------|-----------|-------------|
| `push_to_seed` | `namespace`, `project_name`, `branch`? | Push a branch to the configured seed repository via SSH. |
| `get_seed_status` | `namespace`, `project_name` | Seed sync status, push mode, and vault state. |

## TLS

**GitYard terminates no TLS — by design.** It speaks plain HTTP on every
listener and delegates TLS to an external TLS-terminating reverse proxy
(nginx, Caddy, etc.). There are no `tls` fields in the configuration.

Operational consequences by transport:

- **OAuth transport** (`server.mcp.oauth`) — OAuth requires HTTPS; always
  reached through the TLS proxy.
- **Plain transport with `bearer_auth: true`** — the token rides in the
  `Authorization` header, so this transport **must** sit behind TLS.
- **Plain transport with `bearer_auth: false`** — for **loopback / internal
  use only**. Do not expose beyond the host or a trusted network.

## Backup

A project is an ordinary Git repository under `base_dir`. Back up `base_dir`
as you would any set of Git repositories (filesystem snapshot, `rsync`, or
`git clone`/`git bundle` per project). The SSH key vault (`keys.db`) and user
store (`users.db`) are bbolt databases in `base_dir` — include them in
backups.

## Troubleshooting

| Symptom | Likely cause / fix |
|---------|--------------------|
| Server exits with `storage.base_dir is required` | Set `storage.base_dir` in the config. |
| Server exits with `at least one MCP transport must be configured` | Set `server.mcp.plain.listen` or `server.mcp.oauth.listen`; at least one is required. |
| HTTP 401 on the MCP endpoint | Auth enabled; request lacks a valid `Authorization: Bearer` header. |
| WebSocket upgrade 403 | Auth on and the request `Origin` is not in `allowed_origins`. |
| Seed push fails with "vault is locked" | Server restarted; super-user must unlock via the WebUI Resume banner. |
| First-run wizard does not appear | `server.auth.users.allow_first_run_admin` may be `false`; check the config. |
| Port already in use on startup | Another process holds the listen address; change the port or stop the other process. |
