/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"context"
	"fmt"
	"strings"

	"go.arpabet.com/cligo"
)

// ReleaseCmd implements `gorel release`.
type ReleaseCmd struct {
	Parent  cligo.CliGroup `cli:"group=cli"`
	Version string         `cli:"argument=version"`
	Bump    []string       `cli:"option=bump,short=-b,help=per-module version override module=version (repeatable)"`
	DryRun  bool           `cli:"option=dry-run,help=print the phased release plan and exit"`
}

func (c *ReleaseCmd) Command() string { return "release" }

func (c *ReleaseCmd) Help() (string, string) {
	return "Release one dependency phase of a coordinated multi-module release.",
		`Tags every module in the repository at VERSION (vX.Y.Z), one dependency phase
per run. A "phase" is the set of modules whose in-repo dependencies are already
released; the root module is tagged "vX.Y.Z" and each submodule "<subdir>/vX.Y.Z".

For the phase's modules gorel pins internal require lines to the release version,
strips local-dev replace directives, then runs 'go mod tidy' and 'go build ./...'.
The go.sum is therefore produced by the go toolchain pulling the already-published
dependencies from the real module proxy — gorel computes no checksums itself, so the
sums are exactly what consumers will verify. It then commits and tags locally.

Because each dependent's 'go mod tidy' fetches its dependency from the proxy, the
dependency must be pushed before the next phase runs. So gorel works in phases:

  1. run 'gorel release vX.Y.Z'   — releases the modules with no unreleased deps
  2. push the branch and the tags it printed
  3. run 'gorel release vX.Y.Z' again — releases the next phase, and so on

gorel never pushes: it tags locally and prints the exact 'git push' to publish each
phase, plus the command to run the next one. Re-runs are safe — modules already
tagged at the release version are skipped.

EXAMPLES
  gorel release v1.3.0                  release the next ready phase
  gorel release v1.3.0 -b grpc=v1.3.1   bump grpc to v1.3.1, the rest at v1.3.0
  gorel release v1.3.0 --dry-run        print the full phase plan, change nothing`
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

	// Order modules so every dependency precedes the modules that require it; this
	// also rejects dependency cycles up front.
	mods, err = topoSort(mods)
	if err != nil {
		return err
	}

	cligo.Echo("module prefix: %s", prefix)

	if c.DryRun {
		return c.planDryRun(mods, verOf)
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

	ready, pending := splitReady(mods, verOf)
	if len(ready) == 0 {
		cligo.Echo("every module is already tagged at its release version — nothing to do.")
		return nil
	}

	cligo.Echo("Phase: %s", keysOf(ready))

	// -mod=mod lets tidy rewrite go.mod/go.sum from the published dependencies.
	env := []string{"GOFLAGS=-mod=mod"}
	for _, m := range ready {
		if _, err := rewriteGoMod(m, prefix, pathToKey, verOf, true); err != nil {
			return c.abort(err)
		}
		// Force each in-repo dependency to be re-fetched from the origin so tidy
		// records its real published checksum, not a stale go.sum line or a cache
		// entry an earlier bad release may have poisoned.
		for _, depKey := range m.Deps {
			freshenDep(m.Dir, keyToMod[depKey].Path, verOf(depKey))
		}
	}
	for _, m := range ready {
		if _, err := goCmd(m.Dir, env, "mod", "tidy"); err != nil {
			return c.abort(fmt.Errorf("go mod tidy in %q failed — is every in-repo dependency "+
				"from the previous phase pushed and published?\n%w", m.Key, err))
		}
		if _, err := goCmd(m.Dir, env, "build", "./..."); err != nil {
			return c.abort(fmt.Errorf("go build in %q failed:\n%w", m.Key, err))
		}
		cligo.Echo("  %-26s tidied + built", m.Key)
	}

	if err := commitIfChanged(c.Version); err != nil {
		return err
	}
	created, err := tagAll(ready, verOf)
	if err != nil {
		return err
	}

	c.printNext(created, branch, pending)
	return nil
}

// splitReady partitions the not-yet-released modules into those whose in-repo
// dependencies are all already tagged at the release version (ready: this phase)
// and those still waiting on a dependency (pending: a later phase). Modules
// already tagged at the version are skipped entirely.
func splitReady(mods []Module, verOf func(string) string) (ready, pending []Module) {
	released := make(map[string]bool, len(mods))
	for _, m := range mods {
		if tagExists(m.Tag(verOf(m.Key))) {
			released[m.Key] = true
		}
	}
	for _, m := range mods {
		if released[m.Key] {
			continue
		}
		blocked := false
		for _, d := range m.Deps {
			if !released[d] {
				blocked = true
				break
			}
		}
		if blocked {
			pending = append(pending, m)
		} else {
			ready = append(ready, m)
		}
	}
	return ready, pending
}

// abort undoes this phase's uncommitted go.mod/go.sum edits (best effort) so a
// failed phase leaves a clean tree to retry from, and returns the original error.
func (c *ReleaseCmd) abort(err error) error {
	_, _ = git("checkout", "--", ".")
	return err
}

// printNext reports what this phase tagged and prints the push command plus the
// command to run the next phase (or that the release is complete).
func (c *ReleaseCmd) printNext(created []string, branch string, pending []Module) {
	cligo.Echo("")
	if len(created) == 0 {
		cligo.Echo("no new tags created this phase.")
	} else {
		cligo.Echo("phase complete — tagged %s locally on %q (nothing pushed).",
			strings.Join(created, ", "), branch)
	}
	cligo.Echo("")
	cligo.Echo("Next steps:")
	step := 1
	if len(created) > 0 {
		cligo.Echo("  %d. Publish this phase (push the branch and its tags):", step)
		cligo.Echo("       git push origin %s && git push origin %s", branch, strings.Join(created, " "))
		step++
	}
	if len(pending) > 0 {
		cligo.Echo("  %d. Once those tags are published, run the next phase:", step)
		cligo.Echo("       %s", c.invocation())
		cligo.Echo("")
		cligo.Echo("still waiting (need this phase's tags first): %s", keysOf(pending))
	} else {
		cligo.Echo("  After pushing, the release is complete.")
	}
}

// invocation reproduces the command to run the next phase, preserving any --bump
// overrides so each phase pins the same per-module versions.
func (c *ReleaseCmd) invocation() string {
	parts := []string{"gorel release", c.Version}
	for _, b := range c.Bump {
		parts = append(parts, "-b "+b)
	}
	return strings.Join(parts, " ")
}

// planDryRun prints the full phased plan: each topological layer is one phase,
// released and pushed before the next.
func (c *ReleaseCmd) planDryRun(mods []Module, verOf func(string) string) error {
	layers := topoLayers(mods)
	cligo.Echo("Phased release plan for %s — %d phase(s), push between each:", c.Version, len(layers))
	for i, layer := range layers {
		cligo.Echo("")
		cligo.Echo("  Phase %d:", i+1)
		for _, m := range layer {
			suffix := ""
			if len(m.Deps) > 0 {
				suffix = "  (requires " + strings.Join(m.Deps, ", ") + ")"
			}
			if tagExists(m.Tag(verOf(m.Key))) {
				suffix += "  [already tagged]"
			}
			cligo.Echo("    %-26s -> %s%s", m.Key, m.Tag(verOf(m.Key)), suffix)
		}
	}
	cligo.Echo("")
	cligo.Echo("Each phase pins in-repo requires, runs 'go mod tidy' + 'go build', commits, and")
	cligo.Echo("tags; you push, then run gorel again for the next phase. No checksums are")
	cligo.Echo("computed by gorel — go.sum comes from 'go mod tidy' against the published deps.")
	return nil
}

// topoLayers groups modules by dependency depth: layer 0 has no in-repo deps,
// layer n requires only modules in layers < n. Each layer is one release phase.
// Input is assumed acyclic (topoSort has already rejected cycles).
func topoLayers(mods []Module) [][]Module {
	byKey := make(map[string]Module, len(mods))
	for _, m := range mods {
		byKey[m.Key] = m
	}
	depth := make(map[string]int, len(mods))
	var d func(key string) int
	d = func(key string) int {
		if v, ok := depth[key]; ok {
			return v
		}
		max := -1
		for _, dep := range byKey[key].Deps {
			if dd := d(dep); dd > max {
				max = dd
			}
		}
		depth[key] = max + 1
		return depth[key]
	}
	maxDepth := 0
	for _, m := range mods {
		if dd := d(m.Key); dd > maxDepth {
			maxDepth = dd
		}
	}
	layers := make([][]Module, maxDepth+1)
	for _, m := range mods { // mods is topo-sorted, so order within a layer is stable
		layers[depth[m.Key]] = append(layers[depth[m.Key]], m)
	}
	return layers
}

// keysOf joins module keys for display.
func keysOf(mods []Module) string {
	ks := make([]string, len(mods))
	for i, m := range mods {
		ks[i] = m.Key
	}
	return strings.Join(ks, ", ")
}

// tagAll creates every missing tag for the given modules (skipping existing ones)
// and returns the tags it created.
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

// commitIfChanged stages everything and commits only when there is something to
// commit (a phase whose go.mod/go.sum were already current is a no-op).
func commitIfChanged(version string) error {
	if _, err := git("add", "-A"); err != nil {
		return err
	}
	// `git diff --cached --quiet` exits 0 when nothing is staged, non-zero otherwise.
	if _, err := git("diff", "--cached", "--quiet"); err == nil {
		cligo.Echo("no go.mod/go.sum changes to commit; tagging current HEAD")
		return nil
	}
	_, err := git("commit", "-m", "release "+version)
	return err
}
