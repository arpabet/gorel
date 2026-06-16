# gorel

`gorel` tags **coordinated releases for multi-module Go repositories** — the
`go.arpabet.com/...` style monorepos where a root module and several submodules
(`grpc`, `vrpc`, `providers/badger`, …) move together under one shared version.

It replaces the per-repo `release.sh` scripts: the module prefix is **auto-detected
from `go.mod`**, so the same binary works unchanged in every repo. It is built on
the [cligo](https://go.arpabet.com/cligo) framework and uses
[`golang.org/x/mod`](https://pkg.go.dev/golang.org/x/mod) for precise `go.mod`
edits (no fragile regex) and `git` for tagging.

- **One shared version** moves every module; a single module can take a higher
  patch with `--bump`.
- **Dependency-aware**: the intra-repo dependency graph is detected from `go.mod`;
  modules are released in dependency order and each dependent's `go.sum` is updated
  with its dependency's released checksum before it is tagged.
- **Idempotent**: re-runs skip tags that already exist and tolerate an empty
  release commit — so a newly added submodule can be tagged at an already-released
  version.
- **Safe**: `--dry-run` previews everything (including `go.sum` changes). gorel
  **never pushes** — it tags locally and prints the `git push` for you to run.

## Installation

```bash
# install the released tool
go install go.arpabet.com/gorel@latest

# or run without installing
go run go.arpabet.com/gorel@latest list

# from a local checkout
cd gorel && go install .
```

`gorel` needs `git` on the `PATH` and is run from inside the target repository
(anywhere — it locates the repo root itself).

## Quick start

```bash
cd ~/web/arpabet/servion

gorel list                     # what's released right now?
gorel release v0.4.0 --dry-run # preview the next release, change nothing
gorel release v0.4.0           # pin go.mods + go.sums, commit, tag (does not push)
git push origin main && git push origin v0.4.0 …   # publish (gorel prints this)
```

```
$ gorel list
go.arpabet.com/servion  —  3 module(s)

  MODULE  MODULE PATH                  LATEST
  .       go.arpabet.com/servion       v0.3.0
  grpc    go.arpabet.com/servion/grpc  v0.3.0
  vrpc    go.arpabet.com/servion/vrpc  v0.3.0
```

## Commands

| Command | Purpose |
|---------|---------|
| `gorel release <version> [--bump m=v]… [--dry-run] [--offline]` | Tag a coordinated release of every module, in dependency order. |
| `gorel list [--fetch]` | Show each module and its latest released version (a quick look). |

By default the go toolchain is authoritative for `go.sum`: every releasing module
is served to `go get`/`go mod tidy` from a temporary local proxy (so no tag needs
to be pushed to resolve it), and each dependent is updated, built, and verified.
`--offline` computes the identical `go.sum` checksums locally without invoking the
toolchain — useful when the module proxy is unreachable (gorel falls back to this
automatically and warns).

Global flags from cligo: `--version`/`-v`, `--help`/`-h` (also per command, e.g.
`gorel release --help`).

## Examples

### Release the whole repo at a new version

Moves **every** module to the shared version (a major/minor/patch bump of the
whole repo). The root is tagged `vX.Y.Z`, each submodule `<subdir>/vX.Y.Z`:

```bash
gorel release v1.0.0
# tagged v1.0.0
# tagged grpc/v1.0.0
# tagged vrpc/v1.0.0
# released v1.0.0
```

### Release just one module at its own version

Everything shares the base version, but one module carries an extra change and
takes a higher patch via `--bump` (Go tags must be 3-component semver, so this is
how you express "shared version plus an extra change in module X"):

```bash
gorel release v1.0.0 --bump grpc=v1.0.1
# .     -> v1.0.0 (exists, will skip)
# grpc  -> grpc/v1.0.1
# vrpc  -> vrpc/v1.0.0 (exists, will skip)
# tagged grpc/v1.0.1
```

Because already-existing tags are skipped, if the base version is already out this
effectively releases **only** `grpc`. The same idempotency covers a brand-new
submodule: after adding `vrpc` to an already-released `v0.3.0`, just run

```bash
gorel release v0.3.0     # skips ., grpc; creates only vrpc/v0.3.0
```

### Preview and dry-run

```bash
gorel release v1.0.0 --dry-run
```

```
module prefix: go.arpabet.com/servion
Release plan (shared v1.0.0, dependency order):
  .      -> v1.0.0
  grpc   -> grpc/v1.0.0
  vrpc   -> vrpc/v1.0.0  (requires grpc)

go.mod changes:
  grpc/go.mod: pin go.arpabet.com/servion v0.3.0 -> v1.0.0
  vrpc/go.mod: pin go.arpabet.com/servion v0.3.0 -> v1.0.0

go.sum changes:
  vrpc/go.sum: go.arpabet.com/servion/grpc v1.0.0 …

dry run: nothing committed or tagged
```

After a real release gorel prints the `git push` to publish the branch and tags;
run it yourself when you are ready.

## What it does, in order

1. Validates the version (`vX.Y.Z`; rejects 4-component versions — use `--bump`).
2. Finds the repo root and discovers every `go.mod` (skipping dot-dirs and
   `examples/`); auto-detects the module prefix and the intra-repo dependency
   graph (which modules `require` which other modules in this repo).
3. Orders modules so each dependency comes before the modules that require it,
   then prints the release plan, marking tags that already exist and noting deps.
4. Rewrites each `go.mod`: strips local-dev `replace <prefix>/… => ../…`
   bootstrap directives and pins internal `require <prefix>/…` lines to the
   release version.
5. Brings each dependent's `go.sum` in line with its dependency's released
   checksum — via the go toolchain (default) or locally (`--offline`).
6. Commits the `go.mod`/`go.sum` changes (skipped if there are none).
7. Creates the missing tags only (`git tag -a`), skipping any that exist.
8. Prints the `git push` command. **gorel does not push** — publishing is your
   step, on purpose (it is a tool, not the release authority).

## Notes

- Requires a clean working tree for an actual release (not for `--dry-run` or
  `list`); warns if you are not on `main`.
- A circular dependency between modules is rejected (the release order would be
  undefined).
- `go.work` continues to cover local cross-module development after release; the
  toolchain path sets `GOWORK=off` so it never resolves against unreleased local
  modules.
- Build with version info: `go build -ldflags "-X main.version=v1.0.0 -X main.build=$(git rev-parse --short HEAD)"`.

## License

Apache License 2.0 (Apache-2.0) — Copyright (c) 2026 Karagatan LLC.
