# npmgo — pure Go npm package installer

Reads `package-lock.json`, downloads the pinned tarballs from the npm registry,
verifies their integrity, and extracts them into `node_modules/`. No Node.js,
no npm, no JavaScript runtime — pure Go with zero external dependencies.

## Why

Go projects that embed a web frontend still need npm packages (react, etc.) to
build that frontend. npmgo installs them directly from the lock file, removing
Node.js and npm from the build chain — one static binary does the job.

## Install

```
go install github.com/sopranoworks/npmgo/cmd/npmgo@latest
```

## CLI usage

Run in a directory containing `package-lock.json`:

```
npmgo install                          # install everything into ./node_modules
npmgo install --production             # skip devDependencies
npmgo install --cache-dir /tmp/cache   # use a specific tarball cache
```

Other flags: `--registry URL`, `--token TOKEN` (private registries),
`--concurrency N` (default 8), `--lockfile PATH`, `--target DIR`,
`--timeout DURATION` (default 30s).

Tarballs are cached (content-addressed) under `<user cache>/npmgo` by default,
so repeated installs are near-instant: cached packages are reused and packages
already present at the locked version are skipped.

## Library usage

```go
import "github.com/sopranoworks/npmgo"

err := npmgo.Install(npmgo.Options{
    LockfilePath: "package-lock.json",
    TargetDir:    "node_modules",
})
```

All fields are optional; `Options{}` uses sensible defaults (registry
`https://registry.npmjs.org`, cache `<user cache>/npmgo`, concurrency 8).

## Bundling without node_modules

For Go build pipelines that bundle a frontend, npmgo can populate its tarball
cache without ever extracting a `node_modules` directory, and an esbuild plugin
resolves imports straight from those cached tarballs.

### CacheOnly mode

```go
index, err := npmgo.CachePackages(npmgo.Options{
    LockfilePath: "package-lock.json",
    CacheOnly:    true,
})
// index.Packages maps package names to their cached tarballs.
// No node_modules is created.
```

### esbuild plugin

```go
import (
    "github.com/evanw/esbuild/pkg/api"
    "github.com/sopranoworks/npmgo/esbuildplugin"
)

result := api.Build(api.BuildOptions{
    EntryPoints: []string{"src/main.tsx"},
    Bundle:      true,
    Plugins:     []api.Plugin{esbuildplugin.New(index)},
    // ... bundle config
})
// Bundles directly from the tarball cache — no node_modules.
```

The core module (`github.com/sopranoworks/npmgo`) has **zero external
dependencies**. The esbuild plugin (`github.com/sopranoworks/npmgo/esbuildplugin`)
is a separate, nested Go module that depends on esbuild; import it only if you
need it, and the core installer stays dependency-free.

## Features

- Lock file parsing: `lockfileVersion` 1, 2 and 3
- Integrity verification: SHA-512 (also SHA-256 / SHA-1)
- Content-addressed tarball cache, keyed by integrity hash
- Skips packages already installed at the locked version
- Scoped packages (`@scope/name`) and nested `node_modules`
- Concurrent downloads
- Private registry support via Bearer token
- Cache-only mode + esbuild plugin: bundle a frontend straight from the
  tarball cache, with no `node_modules` on disk

## Limitations

- Requires a `package-lock.json`; it does not resolve dependencies from
  `package.json` or generate a lock file.
- Does not run lifecycle scripts (`preinstall`, `postinstall`, etc.).

## License

MIT — see [LICENSE](LICENSE).
