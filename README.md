# GitCote

[![version](https://img.shields.io/badge/version-1.0.0--rc1-blue)](#)
[![license](https://img.shields.io/badge/license-MIT-green)](LICENSE)

GitCote is an agent-oriented Git host — a dovecote where coding agents come and
go, converging work into one place. Agents push to GitCote over Smart HTTP; an
operator reviews and approves pull requests; GitCote dispatches approved changes
onward to seed (master) repositories via SSH. Built on
[Shoka](https://github.com/sopranoworks/shoka)'s core (OAuth, MCP, user
management, namespace isolation). Pure Go — no external Git binary.

Repositories are isolated on the filesystem as `<base_dir>/<namespace>/<project>`
— each its own Git repository. There is no database for project storage.

## Features

- **Smart HTTP Git hosting** — pure Go via go-git v6; clone, fetch, push without
  an external `git` binary.
- **Pull request system** — create PRs via push options, review, approve, merge,
  view diffs and changed files.
- **Repository browsing via MCP** — 17 tools over Streamable HTTP: file listing,
  file reading, branches, commit log, commit detail, PR management, project
  administration, seed push, and seed status.
- **SSH deploy key management** — namespace-scoped keys encrypted at rest
  (AES-256-GCM with argon2id-derived master key, never persisted to disk).
- **Seed sync** — push to a seed repository on merge, on a periodic schedule, or
  manually. Vault lock/unlock model: after a restart the operator resumes push
  explicitly.
- **OAuth 2.1 authorization server** — CIMD (default), DCR, or confidential
  client registration for external MCP clients.
- **WebUI** — namespace/project management, SSH key management, seed
  configuration, sync status badges, vault resume banner.
- **Multi-user auth** — password + TOTP login, first-run admin bootstrap,
  namespace-scoped grants.

## Install

Four ways to install GitCote, in order of preference for a server:

- **Debian/Ubuntu `.deb` — recommended for Linux servers.** Download the package
  for your architecture (`amd64` or `arm64`) from the
  [GitHub Releases](https://github.com/sopranoworks/gitcote/releases) page and install it:

  ```sh
  sudo apt install ./gitcote_<version>_<arch>.deb
  ```

  This installs the `gitcote` binary to `/usr/bin`, an `/etc/gitcote/gitcote.yaml`
  config, a systemd unit, and a `/var/lib/gitcote` data directory owned by a
  dedicated `gitcote` user (it does **not** auto-start). Then edit the config and
  `sudo systemctl enable --now gitcote`. Full walkthrough:
  [`docs/OPERATIONS.md`](docs/OPERATIONS.md) (*Installation*).

- **`fuigo` — any platform with a Go toolchain.** GitCote embeds a frontend built
  with npmgo + esbuild; `go install` alone cannot run these pre-build steps.
  [fuigo](https://github.com/sopranoworks/fuigo) handles them automatically:

  ```sh
  go install github.com/sopranoworks/fuigo/cmd/fuigo@latest
  fuigo github.com/sopranoworks/gitcote/cmd/gitcote@latest
  ```

  fuigo reads the `fuigo.yaml` in the repository, runs the declared frontend build
  steps (npmgo + esbuild), then `go install`s the result. It lands in `~/go/bin`
  (`$(go env GOBIN)` — put it on your `PATH`).

- **`go install` — without the frontend.** If you only need the Git hosting and
  MCP surface (no web UI):

  ```sh
  go install github.com/sopranoworks/gitcote/cmd/gitcote@latest
  ```

  The binary will work, but the web UI will be empty because `go install` skips
  the frontend build. Use fuigo (above) for a complete install.

- **Homebrew (macOS) — planned.** A source formula for `brew install` /
  `brew services` is planned; none is published yet. On macOS today, use fuigo or
  build from source (*Quick start* below).

To build and run from source for development, see *Quick start* below.

## Quick start

GitCote is a Go program. Build it and run it against a config file:

```sh
go build -o gitcote ./cmd/gitcote
cp gitcote.example.yaml gitcote.yaml      # then edit as needed
./gitcote --config gitcote.yaml
```

Minimal config (required fields only):

```yaml
storage:
  base_dir: "./data"      # project repos are created here
server:
  http:
    listen: ":8080"       # web UI + auth + WebSocket + Git hosting
  mcp:
    plain:                # the plain (internal) MCP transport — served at /mcp
      listen: ":8081"
```

Open http://localhost:8080 — the first-run wizard appears to create the
administrator account. Point an MCP client at the `/mcp` path on the MCP
listener:

```sh
claude mcp add --transport http gitcote http://localhost:8081/mcp
```

The frontend is **pre-built and embedded** in the Go binary — `go build` alone
produces a working binary with the WebUI included. If you modify the frontend
source (`web/`), rebuild it first: `cd web && npm install && npm run build`, then
re-run `go build`.

Authentication is **off** by default (single-operator local mode). See
`gitcote.example.yaml` for the full annotated configuration (auth, OAuth,
logging).

**TLS is outsourced — by design.** GitCote terminates no TLS. Run it behind an
external TLS-terminating reverse proxy (nginx, etc.).

## Documentation

| Audience | Document |
|----------|----------|
| Running & configuring | [`docs/OPERATIONS.md`](docs/OPERATIONS.md) |
| dovefeeder CLI | [`docs/dovefeeder.md`](docs/dovefeeder.md) |

The configuration reference, build instructions, first-run walkthrough, OAuth
setup, Git hosting, SSH key management, seed sync, and MCP tool reference are
all in [`docs/OPERATIONS.md`](docs/OPERATIONS.md). The dovefeeder CLI tool for
spawning coding agents is documented in
[`docs/dovefeeder.md`](docs/dovefeeder.md).

## Tech stack

Go · [mcp-go-sdk](https://github.com/modelcontextprotocol/go-sdk) (MCP over
Streamable HTTP) · [go-git v6](https://github.com/go-git/go-git) (pure Go Git
hosting) · [gorilla/websocket](https://github.com/gorilla/websocket) (WebUI) ·
React + [@shoka/web-core](https://github.com/sopranoworks/shoka) (frontend).

## Version

This is **1.0.0-rc1**. The running binary reports it via `gitcote --version`,
and the MCP server advertises it in `get_server_info`.

## License

GitCote is licensed under the **MIT License** — see [`LICENSE`](LICENSE) for the
full text.
