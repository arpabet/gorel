/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// goCmd runs `go args...` in dir with GOWORK=off (so a stray go.work does not
// substitute local, unreleased module copies for the published ones) plus any
// extra environment. Later values win, so extra overrides the inherited env.
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

// goEnv returns `go env KEY`, or "" on error.
func goEnv(key string) string {
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
