/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
	"golang.org/x/xerrors"
)

// Module is one releasable Go module in the repository.
type Module struct {
	Key  string   // "." for the root module, else the subdir, e.g. "grpc" or "providers/badger"
	Dir  string   // filesystem directory relative to the repo root
	Path string   // module import path, e.g. go.arpabet.com/servion/grpc
	Deps []string // keys of other in-repo modules this one requires (sorted)
}

// Tag returns the git tag for this module at version v: "vX.Y.Z" for the root
// module, "<subdir>/vX.Y.Z" for a submodule (the Go multi-module convention).
func (m Module) Tag(v string) string {
	if m.Key == "." {
		return v
	}
	return m.Key + "/" + v
}

// loadRepo locates the enclosing git repository, changes into its root, and
// returns the auto-detected module prefix plus every releasable module.
func loadRepo() (prefix string, mods []Module, err error) {
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, xerrors.Errorf("not inside a git repository: %w", err)
	}
	if err := os.Chdir(root); err != nil {
		return "", nil, err
	}
	return discoverModules(".")
}

// discoverModules finds every dir containing a go.mod (skipping dot-dirs and
// examples), reads each module path, and derives the shared module prefix from
// the first module.
func discoverModules(root string) (prefix string, mods []Module, err error) {
	var keys []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "examples") {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" {
			rel, rErr := filepath.Rel(root, filepath.Dir(path))
			if rErr != nil {
				return rErr
			}
			keys = append(keys, rel)
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if len(keys) == 0 {
		return "", nil, xerrors.Errorf("no go.mod modules found under %s", root)
	}
	sort.Strings(keys) // "." sorts before any subdir, so the root comes first

	// First pass: read each module's path and its require list.
	reqs := make([][]string, len(keys))
	for i, k := range keys {
		mp, rqs, mErr := parseModule(filepath.Join(root, k, "go.mod"))
		if mErr != nil {
			return "", nil, mErr
		}
		mods = append(mods, Module{Key: k, Dir: k, Path: mp})
		reqs[i] = rqs
	}

	// Second pass: resolve which requires point at other modules in this repo,
	// recording the intra-repo dependency edges (dependent -> dependency keys).
	pathToKey := make(map[string]string, len(mods))
	for _, m := range mods {
		pathToKey[m.Path] = m.Key
	}
	for i := range mods {
		for _, rp := range reqs[i] {
			if depKey, ok := pathToKey[rp]; ok && depKey != mods[i].Key {
				mods[i].Deps = append(mods[i].Deps, depKey)
			}
		}
		sort.Strings(mods[i].Deps)
	}

	prefix = derivePrefix(mods[0].Key, mods[0].Path)
	return prefix, mods, nil
}

// topoSort orders modules so every module appears after all of its in-repo
// dependencies — the order a coordinated release must follow so each dependency
// is tagged (and, online, pushed) before any module that requires it. The input
// order is preserved among independent modules, keeping output deterministic.
// It errors on a dependency cycle.
func topoSort(mods []Module) ([]Module, error) {
	byKey := make(map[string]Module, len(mods))
	for _, m := range mods {
		byKey[m.Key] = m
	}
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(mods))
	var order []Module
	var visit func(m Module) error
	visit = func(m Module) error {
		switch state[m.Key] {
		case done:
			return nil
		case visiting:
			return xerrors.Errorf("dependency cycle involving module %q", m.Key)
		}
		state[m.Key] = visiting
		for _, dk := range m.Deps { // Deps is sorted, so traversal is deterministic
			if dm, ok := byKey[dk]; ok {
				if err := visit(dm); err != nil {
					return err
				}
			}
		}
		state[m.Key] = done
		order = append(order, m)
		return nil
	}
	for _, m := range mods {
		if err := visit(m); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// derivePrefix returns the repo's shared module namespace from the first module:
// the root module's path as-is, or a submodule's path with its subdir stripped.
func derivePrefix(firstKey, firstPath string) string {
	if firstKey == "." {
		return firstPath
	}
	return strings.TrimSuffix(firstPath, "/"+firstKey)
}

// parseModule reads a go.mod and returns its module path and the import paths of
// every require directive (used to discover intra-repo dependency edges).
func parseModule(goMod string) (path string, requires []string, err error) {
	data, err := os.ReadFile(goMod)
	if err != nil {
		return "", nil, err
	}
	f, err := modfile.Parse(goMod, data, nil)
	if err != nil {
		return "", nil, xerrors.Errorf("parse %s: %w", goMod, err)
	}
	if f.Module == nil {
		return "", nil, xerrors.Errorf("%s has no module directive", goMod)
	}
	for _, r := range f.Require {
		requires = append(requires, r.Mod.Path)
	}
	return f.Module.Mod.Path, requires, nil
}

// inRepoRequires returns the in-repo modules that the module in dir requires,
// as depPath -> required version, using pathToKey to recognise in-repo paths.
func inRepoRequires(dir string, pathToKey map[string]string) (map[string]string, error) {
	p := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	f, err := modfile.Parse(p, data, nil)
	if err != nil {
		return nil, xerrors.Errorf("parse %s: %w", p, err)
	}
	out := map[string]string{}
	for _, r := range f.Require {
		if _, ok := pathToKey[r.Mod.Path]; ok {
			out[r.Mod.Path] = r.Mod.Version
		}
	}
	return out, nil
}

// rewriteGoMod strips bootstrap replace directives that point at internal modules
// and pins internal require versions to the release. It returns a human-readable
// list of the changes it would make; with apply=true it also writes the file.
func rewriteGoMod(m Module, prefix string, pathToKey map[string]string, verOf func(string) string, apply bool) (changes []string, err error) {
	p := filepath.Join(m.Dir, "go.mod")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	f, err := modfile.Parse(p, data, nil)
	if err != nil {
		return nil, xerrors.Errorf("parse %s: %w", p, err)
	}

	var drop []*modfile.Replace
	for _, r := range f.Replace {
		if r.Old.Path == prefix || strings.HasPrefix(r.Old.Path, prefix+"/") {
			drop = append(drop, r)
			changes = append(changes, "drop replace "+r.Old.Path)
		}
	}

	type pin struct{ path, to string }
	var pins []pin
	for _, req := range f.Require {
		if key, ok := pathToKey[req.Mod.Path]; ok {
			if to := verOf(key); req.Mod.Version != to {
				pins = append(pins, pin{req.Mod.Path, to})
				changes = append(changes, fmt.Sprintf("pin %s %s -> %s", req.Mod.Path, req.Mod.Version, to))
			}
		}
	}

	if len(changes) == 0 || !apply {
		return changes, nil
	}

	for _, r := range drop {
		if err := f.DropReplace(r.Old.Path, r.Old.Version); err != nil {
			return nil, err
		}
	}
	for _, pn := range pins {
		if err := f.AddRequire(pn.path, pn.to); err != nil {
			return nil, err
		}
	}
	f.Cleanup()
	out, err := f.Format()
	if err != nil {
		return nil, err
	}
	if bytes.Equal(out, data) {
		return changes, nil
	}
	return changes, os.WriteFile(p, out, 0o644)
}

// --- version helpers ---

// validVersion reports whether v is a 3-component semver tag (vX.Y.Z[-pre]).
func validVersion(v string) error {
	if fourPart(v) {
		return xerrors.Errorf("%q has four numbers; Go requires vX.Y.Z — bump a single module with --bump", v)
	}
	if !semver.IsValid(v) {
		return xerrors.Errorf("%q is not valid semver (expected vMAJOR.MINOR.PATCH)", v)
	}
	return nil
}

// fourPart reports whether v looks like v1.2.3.4 (four numeric components).
func fourPart(v string) bool {
	base := strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(base, "-+"); i >= 0 {
		base = base[:i]
	}
	return strings.Count(base, ".") >= 3
}

// parseBumps turns --bump module=version values into a key->version map.
func parseBumps(bumps []string) (map[string]string, error) {
	out := map[string]string{}
	for _, b := range bumps {
		k, v, ok := strings.Cut(b, "=")
		if !ok || k == "" || v == "" {
			return nil, xerrors.Errorf("invalid --bump %q, expected module=version (e.g. grpc=v1.2.3)", b)
		}
		if err := validVersion(v); err != nil {
			return nil, xerrors.Errorf("--bump %q: %w", b, err)
		}
		out[k] = v
	}
	return out, nil
}

// latestVersion returns the highest released version for a module from git tags.
func latestVersion(m Module) (string, bool, error) {
	pattern := "v*"
	if m.Key != "." {
		pattern = m.Key + "/v*"
	}
	out, err := git("tag", "--list", pattern)
	if err != nil {
		return "", false, err
	}
	var best string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		v := line
		if m.Key == "." {
			if strings.Contains(line, "/") { // exclude submodule tags like vrpc/v1.0.0
				continue
			}
		} else {
			v = strings.TrimPrefix(line, m.Key+"/")
			if strings.Contains(v, "/") {
				continue
			}
		}
		if !semver.IsValid(v) {
			continue
		}
		if best == "" || semver.Compare(v, best) > 0 {
			best = v
		}
	}
	return best, best != "", nil
}

// --- git helpers ---

func git(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", xerrors.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func tagExists(tag string) bool {
	return exec.Command("git", "rev-parse", "-q", "--verify", "refs/tags/"+tag).Run() == nil
}

func treeClean() (bool, error) {
	out, err := git("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

func currentBranch() (string, error) {
	return git("rev-parse", "--abbrev-ref", "HEAD")
}
