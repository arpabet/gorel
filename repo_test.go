/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import "testing"

func TestDerivePrefix(t *testing.T) {
	cases := []struct{ key, path, want string }{
		{".", "go.arpabet.com/servion", "go.arpabet.com/servion"},
		{"grpc", "go.arpabet.com/servion/grpc", "go.arpabet.com/servion"},
		{"providers/badger", "go.arpabet.com/store/providers/badger", "go.arpabet.com/store"},
		{"recordbase", "go.arpabet.com/record/recordbase", "go.arpabet.com/record"},
	}
	for _, c := range cases {
		if got := derivePrefix(c.key, c.path); got != c.want {
			t.Errorf("derivePrefix(%q,%q)=%q want %q", c.key, c.path, got, c.want)
		}
	}
}

func TestModuleTag(t *testing.T) {
	if got := (Module{Key: "."}).Tag("v1.2.3"); got != "v1.2.3" {
		t.Errorf("root tag = %q", got)
	}
	if got := (Module{Key: "grpc"}).Tag("v1.2.3"); got != "grpc/v1.2.3" {
		t.Errorf("submodule tag = %q", got)
	}
}

func TestFourPart(t *testing.T) {
	for _, v := range []string{"v1.2.3", "v1.2.3-rc1", "v0.0.1"} {
		if fourPart(v) {
			t.Errorf("%q should not be four-part", v)
		}
	}
	for _, v := range []string{"v1.2.3.4", "v1.2.3.0"} {
		if !fourPart(v) {
			t.Errorf("%q should be four-part", v)
		}
	}
}

func TestValidVersion(t *testing.T) {
	if err := validVersion("v1.2.3"); err != nil {
		t.Errorf("v1.2.3 should be valid: %v", err)
	}
	if err := validVersion("1.2.3"); err == nil {
		t.Error("1.2.3 (no v) should be invalid")
	}
	if err := validVersion("v1.2.3.4"); err == nil {
		t.Error("v1.2.3.4 should be invalid")
	}
}

func TestParseBumps(t *testing.T) {
	m, err := parseBumps([]string{"grpc=v1.2.3", "providers/badger=v1.2.4"})
	if err != nil {
		t.Fatalf("parseBumps: %v", err)
	}
	if m["grpc"] != "v1.2.3" || m["providers/badger"] != "v1.2.4" {
		t.Errorf("unexpected map: %v", m)
	}
	for _, bad := range []string{"grpc", "grpc=", "=v1.2.3", "grpc=1.2.3", "grpc=v1.2.3.4"} {
		if _, err := parseBumps([]string{bad}); err == nil {
			t.Errorf("parseBumps(%q) should fail", bad)
		}
	}
}
