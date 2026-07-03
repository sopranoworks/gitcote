# GitCote

[![version](https://img.shields.io/badge/version-1.0.0--rc1-blue)](#)
[![license](https://img.shields.io/badge/license-MIT-green)](LICENSE)

GitCote is an agent-oriented Git staging yard — a private intermediary between
coding agents and seed (master) repositories. Agents push work to GitCote over
Smart HTTP; an operator reviews and approves pull requests; GitCote pushes
approved changes onward to the seed repository via SSH. Built on
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

The configuration reference, build instructions, first-run walkthrough, OAuth
setup, Git hosting, SSH key management, seed sync, and MCP tool reference are
all in [`docs/OPERATIONS.md`](docs/OPERATIONS.md).

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
