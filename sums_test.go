/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/sumdb/dirhash"
)

// writeMod lays down a minimal but real module (go.mod + one .go file) in dir.
func writeMod(t *testing.T, dir, path string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gomod := "module " + path + "\n\ngo 1.25.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestModuleHashesGoModName guards the c6a18fd footgun: the standalone go.mod
// hash must use the literal filename "go.mod", not the path@version-prefixed
// name that the zip hash uses. Using the wrong name yields a plausible but wrong
// checksum that only fails downstream.
func TestModuleHashesGoModName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tlscamo")
	const path, version = "go.arpabet.com/obfs/tlscamo", "v0.2.0"
	writeMod(t, dir, path)

	_, modHash, err := moduleHashes(path, version, dir)
	if err != nil {
		t.Fatal(err)
	}

	open := func(string) (io.ReadCloser, error) { return os.Open(filepath.Join(dir, "go.mod")) }
	good, _ := dirhash.Hash1([]string{"go.mod"}, open)
	bad, _ := dirhash.Hash1([]string{path + "@" + version + "/go.mod"}, open)

	if modHash != good {
		t.Errorf("go.mod hash = %q, want %q (Hash1 over \"go.mod\")", modHash, good)
	}
	if modHash == bad {
		t.Error("go.mod hash used the path@version-prefixed name — the c6a18fd bug")
	}
}

// TestModuleHashesZipCrossCheck checks the zip hash via an independent code path:
// dirhash.HashDir walks the filesystem, while moduleHashes hashes the built zip.
// They must agree for a clean module directory.
func TestModuleHashesZipCrossCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tlscamo")
	const path, version = "go.arpabet.com/obfs/tlscamo", "v0.2.0"
	writeMod(t, dir, path)

	zipHash, _, err := moduleHashes(path, version, dir)
	if err != nil {
		t.Fatal(err)
	}
	want, err := dirhash.HashDir(dir, path+"@"+version, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	if zipHash != want {
		t.Errorf("zip hash = %q, want %q (HashDir)", zipHash, want)
	}
}

func TestWriteGoSum(t *testing.T) {
	dir := t.TempDir()
	const dep = "go.arpabet.com/obfs/tlscamo"
	// Pre-existing go.sum: a stale dep version plus an unrelated module.
	initial := dep + " v0.1.0 h1:OLDOLDOLD=\n" +
		dep + " v0.1.0/go.mod h1:OLDMODOLD=\n" +
		"github.com/x/y v1.0.0 h1:KEEPKEEP=\n" +
		"github.com/x/y v1.0.0/go.mod h1:KEEPMOD=\n"
	sum := filepath.Join(dir, "go.sum")
	if err := os.WriteFile(sum, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := writeGoSum(dir, dep, "v0.2.0", "h1:NEWZIP=", "h1:NEWMOD=")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected go.sum to change")
	}

	got, _ := os.ReadFile(sum)
	s := string(got)
	for _, must := range []string{
		dep + " v0.2.0 h1:NEWZIP=",
		dep + " v0.2.0/go.mod h1:NEWMOD=",
		"github.com/x/y v1.0.0 h1:KEEPKEEP=", // unrelated lines untouched
	} {
		if !strings.Contains(s, must) {
			t.Errorf("go.sum missing %q\n---\n%s", must, s)
		}
	}
	if strings.Contains(s, "v0.1.0") {
		t.Errorf("stale v0.1.0 lines not dropped\n---\n%s", s)
	}

	// Idempotent: a second identical update reports no change.
	changed, err = writeGoSum(dir, dep, "v0.2.0", "h1:NEWZIP=", "h1:NEWMOD=")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second writeGoSum should be a no-op")
	}
}
