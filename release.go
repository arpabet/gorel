/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.arpabet.com/cligo"
)

// ReleaseCmd implements `gorel release`.
type ReleaseCmd struct {
	Parent  cligo.CliGroup `cli:"group=cli"`
	Version string         `cli:"argument=version"`
	Bump    []string       `cli:"option=bump,short=-b,help=per-module version override module=version (repeatable)"`
	DryRun  bool           `cli:"option=dry-run,help=print the plan, go.mod and go.sum changes, then exit"`
	Offline bool           `cli:"option=offline,help=skip the go toolchain; compute go.sum hashes locally"`
}

func (c *ReleaseCmd) Command() string { return "release" }

func (c *ReleaseCmd) Help() (string, string) {
	return "Tag a coordinated multi-module release.",
		`Tags every module in the repository at VERSION (vX.Y.Z). The module prefix is
auto-detected from go.mod, the root module is tagged "vX.Y.Z" and each submodule
"<subdir>/vX.Y.Z". Internal require lines are pinned to the release version and
local-dev replace directives are stripped before tagging.

Modules are released in dependency order: a module that requires another module
in this repo has its go.sum updated to record the dependency's released checksum
before it is tagged.

By default the go toolchain is authoritative: every releasing module is served to
'go get'/'go mod tidy' from a temporary local proxy (so no tag needs to be pushed
to resolve it), and each dependent is updated, built, and verified. With --offline
the same go.sum hashes are computed locally without invoking the toolchain.

gorel never pushes: it creates the release commit and tags locally and prints the
'git push' command to publish them. Pushing is the operator's action, on purpose.

Re-runs are safe: tags that already exist are skipped (so a newly added submodule
can be tagged at an already-released shared version), and an empty release commit
is tolerated.

EXAMPLES
  gorel release v1.3.0                     release every module at v1.3.0
  gorel release v1.3.0 -b grpc=v1.3.1      everything at v1.3.0, grpc at v1.3.1
  gorel release v1.3.0 --dry-run           preview plan + go.mod/go.sum changes
  gorel release v1.3.0 --offline           compute go.sum locally, no toolchain`
}

func (c *ReleaseCmd) Run(ctx context.Context) error {

	if err := validVersion(c.Version); err != nil {
		return err
	}
	overrides, err := parseBumps(c.Bump)
	if err != nil {
		return err
	}

	prefix, mods, err := loadRepo()
	if err != nil {
		return err
	}

	pathToKey := make(map[string]string, len(mods))
	keyToMod := make(map[string]Module, len(mods))
	for _, m := range mods {
		pathToKey[m.Path] = m.Key
		keyToMod[m.Key] = m
	}
	for k := range overrides {
		if _, ok := keyToMod[k]; !ok {
			return fmt.Errorf("--bump %s: no module with that subdir in this repo (see `gorel list`)", k)
		}
	}
	verOf := func(key string) string {
		if v, ok := overrides[key]; ok {
			return v
		}
		return c.Version
	}

	// Release in dependency order so each dependency is tagged before any module
	// that requires it.
	mods, err = topoSort(mods)
	if err != nil {
		return err
	}

	cligo.Echo("module prefix: %s", prefix)
	cligo.Echo("Release plan (shared %s, dependency order):", c.Version)
	for _, m := range mods {
		t := m.Tag(verOf(m.Key))
		suffix := ""
		if len(m.Deps) > 0 {
			suffix = "  (requires " + strings.Join(m.Deps, ", ") + ")"
		}
		if tagExists(t) {
			cligo.Echo("  %-26s -> %s (exists, will skip)%s", m.Key, t, suffix)
		} else {
			cligo.Echo("  %-26s -> %s%s", m.Key, t, suffix)
		}
	}
	cligo.Echo("")

	if c.DryRun {
		return c.dryRun(mods, prefix, pathToKey, keyToMod, verOf)
	}

	clean, err := treeClean()
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("working tree is dirty; commit or stash first")
	}
	branch, err := currentBranch()
	if err != nil {
		return err
	}
	if branch != "main" {
		cligo.Echo("warning: on branch %q, not 'main'", branch)
	}

	// Choose the go.sum strategy: the authoritative toolchain by default, local
	// hashing when --offline or when the proxy is unreachable (external deps could
	// not be resolved by 'go mod tidy' anyway).
	offline := c.Offline
	if !offline && !proxyReachable() {
		cligo.Echo("warning: module proxy unreachable; computing go.sum locally (offline)")
		offline = true
	}

	if offline {
		return c.releaseOffline(mods, prefix, pathToKey, keyToMod, verOf, branch)
	}
	return c.releaseOnline(mods, prefix, pathToKey, keyToMod, verOf, branch)
}

// dryRun prints the go.mod and go.sum changes the release would make, without
// touching anything. go.sum hashes are computed locally (Strategy B) and are
// exact for leaf dependencies; a dependency that is itself re-pinned shifts its
// own hash, noted inline.
func (c *ReleaseCmd) dryRun(mods []Module, prefix string, pathToKey map[string]string, keyToMod map[string]Module, verOf func(string) string) error {
	cligo.Echo("go.mod changes:")
	any := false
	for _, m := range mods {
		ch, err := rewriteGoMod(m, prefix, pathToKey, verOf, false)
		if err != nil {
			return err
		}
		for _, line := range ch {
			cligo.Echo("  %s/go.mod: %s", m.Key, line)
			any = true
		}
	}
	if !any {
		cligo.Echo("  (none)")
	}

	cligo.Echo("")
	cligo.Echo("go.sum changes:")
	any = false
	for _, m := range mods {
		for _, depKey := range m.Deps {
			dep := keyToMod[depKey]
			zipH, _, err := moduleHashes(dep.Path, verOf(depKey), dep.Dir)
			if err != nil {
				return fmt.Errorf("hash %s: %w", dep.Path, err)
			}
			note := ""
			if len(dep.Deps) > 0 {
				note = "  (approx: dependency is itself re-pinned)"
			}
			cligo.Echo("  %s/go.sum: %s %s %s%s", m.Key, dep.Path, verOf(depKey), zipH, note)
			any = true
		}
	}
	if !any {
		cligo.Echo("  (none)")
	}
	cligo.Echo("")
	cligo.Echo("dry run: nothing committed or tagged")
	return nil
}

// releaseOffline pins every go.mod, computes the in-repo go.sum checksums locally
// (no proxy), commits once, then tags every module.
func (c *ReleaseCmd) releaseOffline(mods []Module, prefix string, pathToKey map[string]string, keyToMod map[string]Module, verOf func(string) string, branch string) error {
	for _, m := range mods {
		if _, err := rewriteGoMod(m, prefix, pathToKey, verOf, true); err != nil {
			return err
		}
	}
	// go.mods are now pinned; hash each dependency from its updated working tree.
	for _, m := range mods {
		for _, depKey := range m.Deps {
			dep := keyToMod[depKey]
			zipH, modH, err := moduleHashes(dep.Path, verOf(depKey), dep.Dir)
			if err != nil {
				return fmt.Errorf("hash %s: %w", dep.Path, err)
			}
			changed, err := writeGoSum(m.Dir, dep.Path, verOf(depKey), zipH, modH)
			if err != nil {
				return err
			}
			if changed {
				cligo.Echo("  %s/go.sum: %s %s", m.Key, dep.Path, verOf(depKey))
			}
		}
	}

	if err := commitIfChanged(c.Version); err != nil {
		return err
	}

	created, err := tagAll(mods, verOf)
	if err != nil {
		return err
	}
	return c.finishRelease(created, branch)
}

// releaseOnline pins every go.mod, then lets the go toolchain be authoritative:
// it serves every releasing module to the toolchain from a temporary local proxy
// (so no tag needs to be pushed to resolve it) and, in dependency order, updates
// each dependent with 'go get'/'go mod tidy', builds it, and verifies its go.sum.
// Everything is then committed once and tagged. Nothing is pushed.
func (c *ReleaseCmd) releaseOnline(mods []Module, prefix string, pathToKey map[string]string, keyToMod map[string]Module, verOf func(string) string, branch string) error {
	for _, m := range mods {
		if _, err := rewriteGoMod(m, prefix, pathToKey, verOf, true); err != nil {
			return err
		}
	}

	proxyDir, err := buildLocalProxy(mods, verOf)
	if err != nil {
		return fmt.Errorf("build local proxy: %w", err)
	}
	defer os.RemoveAll(proxyDir)
	env := proxyEnv(proxyDir)

	for _, m := range mods {
		if len(m.Deps) == 0 {
			continue
		}
		for _, depKey := range m.Deps {
			dep := keyToMod[depKey]
			if _, err := goCmd(m.Dir, env, "get", dep.Path+"@"+verOf(depKey)); err != nil {
				return err
			}
		}
		if _, err := goCmd(m.Dir, env, "mod", "tidy"); err != nil {
			return err
		}
		if _, err := goCmd(m.Dir, env, "build", "./..."); err != nil {
			return err
		}
		if _, err := goCmd(m.Dir, env, "mod", "verify"); err != nil {
			return err
		}
		cligo.Echo("  %s: go.sum updated, built, and verified", m.Key)
	}

	if err := commitIfChanged(c.Version); err != nil {
		return err
	}
	created, err := tagAll(mods, verOf)
	if err != nil {
		return err
	}
	return c.finishRelease(created, branch)
}

// tagAll creates every missing tag (skipping existing ones) and returns the tags
// it created.
func tagAll(mods []Module, verOf func(string) string) ([]string, error) {
	var created []string
	for _, m := range mods {
		t := m.Tag(verOf(m.Key))
		if tagExists(t) {
			cligo.Echo("tag %s already exists; skipping", t)
			continue
		}
		if _, err := git("tag", "-a", t, "-m", t); err != nil {
			return nil, err
		}
		cligo.Echo("tagged %s", t)
		created = append(created, t)
	}
	return created, nil
}

// finishRelease reports what was created locally and prints the command to
// publish it. gorel deliberately does not push: tagging is the tool's job,
// pushing is the operator's.
func (c *ReleaseCmd) finishRelease(created []string, branch string) error {
	if len(created) == 0 {
		cligo.Echo("no new tags to create; nothing to release")
		return nil
	}
	cligo.Echo("")
	cligo.Echo("created %d tag(s) locally on %q; nothing pushed.", len(created), branch)
	cligo.Echo("publish with:")
	cligo.Echo("  git push origin %s && git push origin %s", branch, strings.Join(created, " "))
	return nil
}

// commitIfChanged stages everything and commits only when there is something to
// commit (the go.mod rewrites may be a no-op for an already-released repo).
func commitIfChanged(version string) error {
	if _, err := git("add", "-A"); err != nil {
		return err
	}
	// `git diff --cached --quiet` exits 0 when nothing is staged, non-zero otherwise.
	if _, err := git("diff", "--cached", "--quiet"); err == nil {
		cligo.Echo("no go.mod changes to commit; tagging current HEAD")
		return nil
	}
	_, err := git("commit", "-m", "release "+version)
	return err
}
