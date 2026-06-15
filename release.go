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
	DryRun  bool           `cli:"option=dry-run,help=print the plan and go.mod changes, then exit"`
	NoPush  bool           `cli:"option=no-push,help=create the commit and tags locally but do not push"`
}

func (c *ReleaseCmd) Command() string { return "release" }

func (c *ReleaseCmd) Help() (string, string) {
	return "Tag a coordinated multi-module release.",
		`Tags every module in the repository at VERSION (vX.Y.Z). The module prefix is
auto-detected from go.mod, the root module is tagged "vX.Y.Z" and each submodule
"<subdir>/vX.Y.Z". Internal require lines are pinned to the release version and
local-dev replace directives are stripped before tagging.

Re-runs are safe: tags that already exist are skipped (so a newly added submodule
can be tagged at an already-released shared version), and an empty release commit
is tolerated.

EXAMPLES
  gorel release v1.3.0                     release every module at v1.3.0
  gorel release v1.3.0 -b grpc=v1.3.1      everything at v1.3.0, grpc at v1.3.1
  gorel release v1.3.0 --dry-run           preview the plan, change nothing
  gorel release v1.3.0 --no-push           tag locally, push by hand`
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
	keys := make(map[string]bool, len(mods))
	for _, m := range mods {
		pathToKey[m.Path] = m.Key
		keys[m.Key] = true
	}
	for k := range overrides {
		if !keys[k] {
			return fmt.Errorf("--bump %s: no module with that subdir in this repo (see `gorel list`)", k)
		}
	}
	verOf := func(key string) string {
		if v, ok := overrides[key]; ok {
			return v
		}
		return c.Version
	}

	cligo.Echo("module prefix: %s", prefix)
	cligo.Echo("Release plan (shared %s):", c.Version)
	for _, m := range mods {
		t := m.Tag(verOf(m.Key))
		if tagExists(t) {
			cligo.Echo("  %-26s -> %s (exists, will skip)", m.Key, t)
		} else {
			cligo.Echo("  %-26s -> %s", m.Key, t)
		}
	}
	cligo.Echo("")

	if c.DryRun {
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
		cligo.Echo("dry run: nothing committed or tagged")
		return nil
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

	for _, m := range mods {
		if _, err := rewriteGoMod(m, prefix, pathToKey, verOf, true); err != nil {
			return err
		}
	}

	if err := commitIfChanged(c.Version); err != nil {
		return err
	}

	var created []string
	for _, m := range mods {
		t := m.Tag(verOf(m.Key))
		if tagExists(t) {
			cligo.Echo("tag %s already exists; skipping", t)
			continue
		}
		if _, err := git("tag", "-a", t, "-m", t); err != nil {
			return err
		}
		cligo.Echo("tagged %s", t)
		created = append(created, t)
	}

	if len(created) == 0 {
		cligo.Echo("no new tags to create; nothing to release")
		return nil
	}

	if c.NoPush {
		cligo.Echo("--no-push: created tag(s) locally; not pushed: %s", strings.Join(created, " "))
		cligo.Echo("  git push origin %s && git push origin %s", branch, strings.Join(created, " "))
		return nil
	}

	if _, err := git("push", "origin", branch); err != nil {
		return err
	}
	if _, err := git(append([]string{"push", "origin"}, created...)...); err != nil {
		return err
	}
	cligo.Echo("released %s", c.Version)
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
