/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: BUSL-1.1
 */

package main

import (
	"context"

	"go.arpabet.com/cligo"
)

// ListCmd implements `gorel list`.
type ListCmd struct {
	Parent cligo.CliGroup `cli:"group=cli"`
	Fetch  bool           `cli:"option=fetch,short=-f,help=run 'git fetch --tags' first so versions are up to date"`
}

func (c *ListCmd) Command() string { return "list" }

func (c *ListCmd) Help() (string, string) {
	return "List modules and their latest released version.",
		`Shows every releasable module in the repository next to the highest version tag
found for it — a quick look at where each module stands. Modules without a tag
yet show "(unreleased)". Pass --fetch to refresh tags from the remote first.

EXAMPLE
  gorel list
  gorel list --fetch`
}

func (c *ListCmd) Run(ctx context.Context) error {

	prefix, mods, err := loadRepo()
	if err != nil {
		return err
	}

	if c.Fetch {
		if _, err := git("fetch", "--tags", "--quiet"); err != nil {
			cligo.Echo("warning: git fetch failed: %v", err)
		}
	}

	cligo.Echo("%s  —  %d module(s)", prefix, len(mods))
	cligo.Echo("")

	keyW, pathW := len("MODULE"), len("MODULE PATH")
	for _, m := range mods {
		if len(m.Key) > keyW {
			keyW = len(m.Key)
		}
		if len(m.Path) > pathW {
			pathW = len(m.Path)
		}
	}

	cligo.Echo("  %-*s  %-*s  %s", keyW, "MODULE", pathW, "MODULE PATH", "LATEST")
	for _, m := range mods {
		v, ok, err := latestVersion(m)
		if err != nil {
			return err
		}
		if !ok {
			v = "(unreleased)"
		}
		cligo.Echo("  %-*s  %-*s  %s", keyW, m.Key, pathW, m.Path, v)
	}
	return nil
}
