/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"go.arpabet.com/cligo"
)

// RepairCmd implements `gorel repair`.
type RepairCmd struct {
	Parent cligo.CliGroup `cli:"group=cli"`
	DryRun bool           `cli:"option=dry-run,help=report which modules' go.mod/go.sum would change, without modifying them"`
}

func (c *RepairCmd) Command() string { return "repair" }

func (c *RepairCmd) Help() (string, string) {
	return "Repair every module's go.sum against its published dependencies.",
		`Runs 'go mod tidy' in each module so go.mod/go.sum are recomputed from the real
module proxy / VCS. This is the fix when a go.sum records a stale or wrong checksum
— e.g. a release left a dependent pinned to a hash the proxy never served, so
'go build' fails with "checksum mismatch ... SECURITY ERROR".

If 'go mod tidy' fails because go.sum already holds a conflicting hash, repair
drops that module's go.sum and regenerates it from scratch.

Every in-repo dependency must already be published (tagged and pushed) so the proxy
can serve it; repair is a post-release fix, not part of releasing.

gorel never pushes and does not commit: it updates the working tree and leaves
committing to you.

EXAMPLES
  gorel repair             tidy every module, rewriting go.sum where needed
  gorel repair --dry-run   report what would change, then restore the files`
}

func (c *RepairCmd) Run(ctx context.Context) error {
	prefix, mods, err := loadRepo()
	if err != nil {
		return err
	}
	cligo.Echo("module prefix: %s", prefix)

	// -mod=mod lets tidy rewrite go.mod/go.sum; GOWORK=off (added by goCmd) keeps a
	// stray go.work from substituting local, possibly-stale module copies.
	env := []string{"GOFLAGS=-mod=mod"}

	changedAny := false
	for _, m := range mods {
		changed, err := repairModule(m, env, c.DryRun)
		if err != nil {
			return err
		}
		switch {
		case changed && c.DryRun:
			cligo.Echo("  %-26s would change", m.Key)
		case changed:
			cligo.Echo("  %-26s updated", m.Key)
		default:
			cligo.Echo("  %-26s up to date", m.Key)
		}
		changedAny = changedAny || changed
	}

	cligo.Echo("")
	switch {
	case c.DryRun:
		cligo.Echo("dry run: nothing modified")
	case changedAny:
		cligo.Echo("go.sum repaired; review and commit the changes.")
	default:
		cligo.Echo("everything already up to date.")
	}
	return nil
}

// repairModule tidies one module so its go.mod/go.sum match the published deps,
// reporting whether anything changed. In dry-run it tidies for real (the only way
// to know the outcome) then restores the original go.mod/go.sum so nothing
// persists.
func repairModule(m Module, env []string, dryRun bool) (changed bool, err error) {
	modPath := filepath.Join(m.Dir, "go.mod")
	sumPath := filepath.Join(m.Dir, "go.sum")
	modBefore, err := os.ReadFile(modPath)
	if err != nil {
		return false, err
	}
	sumBefore, err := os.ReadFile(sumPath)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}

	if err := tidyModule(m.Dir, env); err != nil {
		return false, err
	}

	modAfter, _ := os.ReadFile(modPath)
	sumAfter, _ := os.ReadFile(sumPath)
	changed = !bytes.Equal(modBefore, modAfter) || !bytes.Equal(sumBefore, sumAfter)

	if dryRun && changed {
		if err := os.WriteFile(modPath, modBefore, 0o644); err != nil {
			return false, err
		}
		if sumBefore == nil {
			if err := os.Remove(sumPath); err != nil && !os.IsNotExist(err) {
				return false, err
			}
		} else if err := os.WriteFile(sumPath, sumBefore, 0o644); err != nil {
			return false, err
		}
	}
	return changed, nil
}

// tidyModule runs `go mod tidy` in dir. A go.sum that already records a conflicting
// hash makes tidy fail (it verifies before rewriting), so on failure we drop go.sum
// and retry — regenerating it from the proxy is exactly the repair we want.
func tidyModule(dir string, env []string) error {
	if _, err := goCmd(dir, env, "mod", "tidy"); err == nil {
		return nil
	}
	if err := os.Remove(filepath.Join(dir, "go.sum")); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, err := goCmd(dir, env, "mod", "tidy")
	return err
}
