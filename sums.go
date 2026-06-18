/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
	modzip "golang.org/x/mod/zip"
)

// moduleHashes computes the two go.sum hashes for the module path@version whose
// source lives in dir — the same h1: values `go mod download` records:
//
//	zipHash: dirhash.Hash1 over the module zip (golang.org/x/mod/zip layout)
//	modHash: dirhash.Hash1 over the single synthetic file named "go.mod"
//
// dir must be the working tree gorel is about to commit and tag, so the hashes
// match what the proxy will later serve from that tag. Both libraries are the
// ones the go command itself uses, so the result is byte-identical (verified
// against a real go.sum in sums_test.go).
func moduleHashes(path, version, dir string) (zipHash, modHash string, err error) {
	zf, err := os.CreateTemp("", "gorel-*.zip")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(zf.Name())
	mv := module.Version{Path: path, Version: version}
	if err := moduleZip(zf, mv, dir); err != nil {
		zf.Close()
		return "", "", fmt.Errorf("zip %s@%s from %s: %w", path, version, dir, err)
	}
	if err := zf.Close(); err != nil {
		return "", "", err
	}
	if zipHash, err = dirhash.HashZip(zf.Name(), dirhash.Hash1); err != nil {
		return "", "", err
	}
	goMod := filepath.Join(dir, "go.mod")
	// The standalone go.mod hash uses the literal name "go.mod", NOT the
	// path@version-prefixed name the zip hash uses — getting this wrong yields a
	// plausible-but-wrong checksum that only fails downstream (the c6a18fd bug).
	modHash, err = dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return os.Open(goMod)
	})
	return zipHash, modHash, err
}

// moduleZip writes the module zip for mv to w, containing exactly the files git
// would commit from dir (tracked + new, minus ignored) — matching what the proxy
// serves from the eventual tag. Hashing the raw working tree with CreateFromDir
// instead folds in git-ignored junk (.idea/, .claude/, local scratch files), so
// the checksum verifies locally but mismatches the proxy downstream: that is the
// value-rpc v1.4.0 release bug. CreateFromDir over the staged tree still applies
// the module-zip rules, so nested modules and vendor dirs git happens to list are
// dropped here. Outside a git work tree (unit tests) it falls back to the raw dir.
func moduleZip(w io.Writer, mv module.Version, dir string) error {
	files, ok, err := gitCommitFiles(dir)
	if err != nil {
		return err
	}
	if !ok {
		return modzip.CreateFromDir(w, mv, dir)
	}
	stage, err := os.MkdirTemp("", "gorel-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, rel := range files {
		if err := copyForZip(filepath.Join(dir, rel), filepath.Join(stage, rel)); err != nil {
			return err
		}
	}
	return modzip.CreateFromDir(w, mv, stage)
}

// gitCommitFiles lists, relative to dir, the files git would include in a commit
// of dir — tracked files plus new non-ignored files, with the working-tree
// content. ok is false when dir is not inside a git work tree, signalling the
// caller to fall back to hashing the directory as-is.
func gitCommitFiles(dir string) (files []string, ok bool, err error) {
	if exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Run() != nil {
		return nil, false, nil
	}
	out, err := exec.Command("git", "-C", dir, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, false, err
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			files = append(files, p)
		}
	}
	return files, true, nil
}

// copyForZip copies the regular file src to dst (creating parents), mirroring how
// module zips treat the tree: a tracked-but-deleted file (gone from the working
// tree) is skipped, as it would be from the commit, and symlinks/irregular files
// are omitted just as modzip omits them.
func copyForZip(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// writeGoSum updates dir/go.sum so that depPath is recorded at exactly version
// with the given hashes, dropping any other-version lines for depPath (a module
// requires a single version of each dependency). Other modules' lines are left
// untouched. Reports whether the file changed.
func writeGoSum(dir, depPath, version, zipHash, modHash string) (changed bool, err error) {
	p := filepath.Join(dir, "go.sum")
	data, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}

	want := map[string]bool{
		fmt.Sprintf("%s %s %s", depPath, version, zipHash):        true,
		fmt.Sprintf("%s %s/go.mod %s", depPath, version, modHash): true,
	}
	prefix := depPath + " "

	var kept []string
	have := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			if want[line] {
				have[line] = true // a correct line already present — keep it
				kept = append(kept, line)
			}
			// drop every other line for depPath (stale versions / wrong hashes)
			continue
		}
		kept = append(kept, line)
	}
	for line := range want {
		if !have[line] {
			kept = append(kept, line)
		}
	}

	sort.Strings(kept) // go.sum is kept sorted (path, then version; "v" < "v.../go.mod")
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	if out == string(data) {
		return false, nil
	}
	return true, os.WriteFile(p, []byte(out), 0o644)
}

// --- go toolchain helpers (Strategy A) ---

// goCmd runs `go args...` in dir with GOWORK=off (so a stray go.work does not
// pull in local, unreleased modules) plus any extra environment (e.g. the local
// proxy settings from proxyEnv). Later values win, so extra overrides the inherited
// environment.
func goCmd(dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("go %s (in %s): %v\n%s",
			strings.Join(args, " "), dir, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// buildLocalProxy writes a temporary file:// GOPROXY containing every releasing
// module at its target version, served from its (already pinned) working tree.
// This lets the go toolchain resolve in-repo dependencies authoritatively while
// gorel never pushes — the tags need not even exist yet. The zip is built with
// the same primitive as moduleHashes, so the checksums the toolchain records are
// identical to what the eventual pushed tag will produce. Caller removes the dir.
func buildLocalProxy(mods []Module, verOf func(string) string) (string, error) {
	dir, err := os.MkdirTemp("", "gorel-proxy-")
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, m := range mods {
		ver := verOf(m.Key)
		escPath, err := module.EscapePath(m.Path)
		if err != nil {
			return "", err
		}
		escVer, err := module.EscapeVersion(ver)
		if err != nil {
			return "", err
		}
		vdir := filepath.Join(dir, filepath.FromSlash(escPath), "@v")
		if err := os.MkdirAll(vdir, 0o755); err != nil {
			return "", err
		}
		goMod, err := os.ReadFile(filepath.Join(m.Dir, "go.mod"))
		if err != nil {
			return "", err
		}
		writes := map[string][]byte{
			escVer + ".mod":  goMod,
			escVer + ".info": []byte(fmt.Sprintf("{\"Version\":%q,\"Time\":%q}\n", ver, now)),
			"list":           []byte(ver + "\n"),
		}
		for name, data := range writes {
			if err := os.WriteFile(filepath.Join(vdir, name), data, 0o644); err != nil {
				return "", err
			}
		}
		zf, err := os.Create(filepath.Join(vdir, escVer+".zip"))
		if err != nil {
			return "", err
		}
		if err := moduleZip(zf, module.Version{Path: m.Path, Version: ver}, m.Dir); err != nil {
			zf.Close()
			return "", err
		}
		if err := zf.Close(); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// proxyEnv returns the environment that makes the go toolchain resolve in-repo
// modules from the local proxy while still reaching the real proxy for external
// dependencies. GONOPROXY=none overrides any GOPRIVATE/GONOPROXY that would
// otherwise force direct VCS (and miss the unpushed tags); the user's GONOSUMDB
// is left intact so external modules are still checked against the sum database.
func proxyEnv(proxyDir string) []string {
	orig := goEnv("GOPROXY")
	if orig == "" || orig == "off" {
		orig = "https://proxy.golang.org,direct"
	}
	return []string{
		"GOPROXY=file://" + filepath.ToSlash(proxyDir) + "," + orig,
		"GONOPROXY=none",
		"GOFLAGS=-mod=mod",
	}
}

// proxyReachable reports whether the module proxy looks usable, so we can pick
// the authoritative toolchain path (Strategy A) when online and fall back to
// local hashing (Strategy B) when not. GOPROXY=off means deliberately offline;
// a bare "direct" means VCS access, which we optimistically treat as online.
func proxyReachable() bool {
	goproxy := strings.TrimSpace(goEnv("GOPROXY"))
	if goproxy == "off" {
		return false
	}
	first := goproxy
	if i := strings.IndexAny(goproxy, ",|"); i >= 0 {
		first = goproxy[:i]
	}
	first = strings.TrimSpace(first)
	if first == "" || first == "direct" {
		return reachableHost("https://proxy.golang.org")
	}
	if strings.HasPrefix(first, "file://") {
		return true // local filesystem proxy
	}
	return reachableHost(first)
}

func reachableHost(url string) bool {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Head(url)
	if err != nil {
		// Some proxies reject HEAD; a reachable host is enough to decide "online".
		return false
	}
	resp.Body.Close()
	return true
}

func goEnv(key string) string {
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
