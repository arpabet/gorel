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
- **Phased by dependency**: the intra-repo dependency graph is detected from
  `go.mod`. Each `gorel release` run tags the modules whose in-repo dependencies
  are already published; you push, then run it again for the next phase.
- **No custom checksums**: `go.sum` is produced by `go mod tidy` pulling each
  already-published dependency from the real module proxy, so the sums are exactly
  what consumers will verify. gorel never computes a hash itself.
- **Idempotent**: re-runs skip tags that already exist, so a phase is safe to
  repeat and a newly added submodule can be tagged at an already-released version.
- **Safe**: `--dry-run` prints the full phase plan. gorel **never pushes** — it
  tags locally and prints the `git push` plus the next-phase command for you.
- **Self-repairing**: `gorel repair` rebuilds each module's `go.sum` from the
  published modules when a checksum goes stale or wrong.

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
gorel release v0.4.0 --dry-run # preview the full phase plan, change nothing

gorel release v0.4.0           # phase 1: tag the modules with no unreleased deps
git push origin main && git push origin v0.4.0 …   # publish (gorel prints this)
gorel release v0.4.0           # phase 2: tag the next layer, and so on
git push origin main && git push origin grpc/v0.4.0 …
```

Each phase pins in-repo `require`s, runs `go mod tidy` + `go build`, commits, and
tags locally; gorel then prints exactly what to push and the command for the next
phase. Repeat until it reports the release is complete.

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
| `gorel release <version> [--bump m=v]… [--dry-run]` | Release the next ready dependency phase (run repeatedly, pushing between phases). |
| `gorel list [--fetch]` | Show each module and its latest released version (a quick look). |
| `gorel repair [--dry-run]` | Repair every module's `go.sum` against its **published** dependencies. |

`gorel release` runs **one dependency phase per invocation**. A phase is the set of
modules whose in-repo dependencies are already tagged. For those modules gorel pins
internal `require`s, strips local-dev `replace`s, then runs `go mod tidy` and
`go build ./...` — so `go.sum` is written by the toolchain pulling the published
dependencies from the real proxy. It commits and tags locally, then prints the
`git push` to publish the phase and the command to run the next one. Because each
dependent's `go mod tidy` fetches its dependency from the proxy, **you must push a
phase before running the next**. gorel computes no checksums itself.

Global flags from cligo: `--version`/`-v`, `--help`/`-h` (also per command, e.g.
`gorel release --help`).

## Examples

### Release the whole repo at a new version

Moves **every** module to the shared version (the root is tagged `vX.Y.Z`, each
submodule `<subdir>/vX.Y.Z`), one dependency phase at a time. With `vrpc` requiring
`grpc`, that is two phases:

```bash
gorel release v1.0.0
# Phase: ., grpc
# tagged v1.0.0
# tagged grpc/v1.0.0
# Next steps:
#   1. git push origin main && git push origin v1.0.0 grpc/v1.0.0
#   2. gorel release v1.0.0          # for the next phase

git push origin main && git push origin v1.0.0 grpc/v1.0.0

gorel release v1.0.0
# Phase: vrpc
# tagged vrpc/v1.0.0
# After pushing, the release is complete.

git push origin main && git push origin vrpc/v1.0.0
```

### Release just one module at its own version

Everything shares the base version, but one module carries an extra change and
takes a higher patch via `--bump` (Go tags must be 3-component semver, so this is
how you express "shared version plus an extra change in module X"). Pass the same
`--bump` on every phase so each one pins consistently:

```bash
gorel release v1.0.0 --bump grpc=v1.0.1
```

Already-existing tags are skipped, so if the base version is already out this
effectively releases **only** the bumped module. The same idempotency covers a
brand-new submodule: after adding `vrpc` to an already-released `v0.3.0`, just run
`gorel release v0.3.0` — it skips the tagged modules and creates only `vrpc/v0.3.0`.

### Preview and dry-run

```bash
gorel release v1.0.0 --dry-run
```

```
module prefix: go.arpabet.com/servion
Phased release plan for v1.0.0 — 2 phase(s), push between each:

  Phase 1:
    .                          -> v1.0.0
    grpc                       -> grpc/v1.0.0

  Phase 2:
    vrpc                       -> vrpc/v1.0.0  (requires grpc)

Each phase pins in-repo requires, runs 'go mod tidy' + 'go build', commits, and
tags; you push, then run gorel again for the next phase. No checksums are
computed by gorel — go.sum comes from 'go mod tidy' against the published deps.
```

After each real phase gorel prints the `git push` to publish it and the command to
run the next phase; run them yourself when you are ready.

### Repair a stale or wrong `go.sum`

If a build fails with a checksum mismatch on an in-repo dependency —

```
verifying go.arpabet.com/value-rpc@v1.4.0: checksum mismatch
  downloaded: h1:d0ij…
  go.sum:     h1:TY5b…
SECURITY ERROR
```

— the recorded checksum no longer matches what the proxy serves. `gorel repair`
re-fetches each in-repo dependency from the origin (clearing any stale `go.sum`
line and poisoned cache entry) and runs `go mod tidy` in every module, so `go.sum`
is rewritten from the **published** modules:

```bash
gorel repair             # tidy every module, rewriting go.sum where needed
gorel repair --dry-run   # report what would change, then restore the files
```

```
module prefix: go.arpabet.com/value-rpc
  .                          up to date
  quic                       updated
  resilience                 updated

go.sum repaired; review and commit the changes.
```

Every in-repo dependency must already be tagged **and pushed** so the proxy can
serve it — `repair` is a post-release fix, not part of releasing. Like `release`,
it never commits or pushes; review the working-tree changes and commit them
yourself.

## What it does, in each phase

1. Validates the version (`vX.Y.Z`; rejects 4-component versions — use `--bump`).
2. Finds the repo root and discovers every `go.mod` (skipping dot-dirs and
   `examples/`); auto-detects the module prefix and the intra-repo dependency
   graph (which modules `require` which other modules in this repo).
3. Selects the **ready** modules: those not yet tagged at the release version whose
   in-repo dependencies are all already tagged (the rest wait for a later phase).
4. For each ready module: rewrites `go.mod` (strips local-dev
   `replace <prefix>/… => ../…` directives, pins internal `require <prefix>/…`
   lines to the release version), then re-fetches each in-repo dependency from the
   origin so a stale `go.sum` line or poisoned cache entry can't survive.
5. Runs `go mod tidy` and `go build ./...` in each ready module — the toolchain
   pulls the published dependencies and writes the verified `go.sum`. gorel
   computes no checksums itself.
6. Commits the `go.mod`/`go.sum` changes (skipped if there are none).
7. Creates the missing tags only (`git tag -a`), skipping any that exist.
8. Prints the `git push` to publish the phase and the command to run the next
   phase. **gorel does not push** — publishing is your step, on purpose, and is
   what makes each phase's dependency available to the next.

## Notes

- Run `gorel release` once per phase, pushing between phases; deps must be
  published before the next phase's `go mod tidy` can resolve them. If a phase's
  `go mod tidy` fails, the most likely cause is the previous phase not being pushed.
- Requires a clean working tree for an actual release (not for `--dry-run` or
  `list`); warns if you are not on `main`. A failed phase restores the working tree.
- A circular dependency between modules is rejected (no release order exists).
- `go.work` continues to cover local cross-module development; gorel sets
  `GOWORK=off` for its `go` commands so they never resolve against unreleased
  local modules.
- Build with version info: `go build -ldflags "-X main.version=v1.0.0 -X main.build=$(git rev-parse --short HEAD)"`.

## License

Apache License 2.0 (Apache-2.0) — Copyright (c) 2026 Karagatan LLC.
