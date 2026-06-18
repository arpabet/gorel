/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/module"
)

// freshenDep makes the next `go mod tidy` in dir re-fetch depPath@version from the
// origin and record its real published checksum. It removes both the stale go.sum
// entry (which would otherwise be trusted, or collide) and any cached copy (which
// an earlier bad release may have poisoned with the wrong bits) — the two things
// that let a wrong in-repo checksum survive a plain tidy.
func freshenDep(dir, depPath, version string) {
	_ = dropDepFromGoSum(dir, depPath, version)
	evictFromModCache(depPath, version)
}

// dropDepFromGoSum removes the two go.sum lines (zip and /go.mod) recording
// depPath at version from dir/go.sum, leaving every other line untouched.
func dropDepFromGoSum(dir, depPath, version string) error {
	p := filepath.Join(dir, "go.sum")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == depPath && (f[1] == version || f[1] == version+"/go.mod") {
			continue
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(p, []byte(out), 0o644)
}

// evictFromModCache removes depPath@version from the local module cache (download
// files plus the extracted tree) so the next fetch comes from the origin. Cache
// entries are read-only, so it makes paths writable before removing them. Missing
// entries are ignored.
func evictFromModCache(depPath, version string) {
	cache := goEnv("GOMODCACHE")
	if cache == "" {
		return
	}
	esc, err := module.EscapePath(depPath)
	if err != nil {
		return
	}
	escV, err := module.EscapeVersion(version)
	if err != nil {
		return
	}
	dl := filepath.Join(cache, "cache", "download", filepath.FromSlash(esc), "@v")
	for _, ext := range []string{".info", ".mod", ".zip", ".ziphash", ".lock"} {
		forceRemove(filepath.Join(dl, escV+ext))
	}
	forceRemove(filepath.Join(cache, filepath.FromSlash(esc)+"@"+escV))
}

// forceRemove deletes path (file or tree), first making every entry writable so
// the read-only module cache can be removed. Errors are ignored — a missing or
// unremovable cache entry is not fatal to a release.
func forceRemove(path string) {
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(path)
}
