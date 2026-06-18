/*
 * Copyright (c) 2026 Karagatan LLC.
 * SPDX-License-Identifier: Apache-2.0
 */

// Command gorel tags coordinated releases for go.arpabet.com-style multi-module
// Go repositories. See README.md for details.
package main

import "go.arpabet.com/cligo"

// Overridable at build time:
//
//	go build -ldflags "-X main.version=v1.0.0 -X main.build=$(git rev-parse --short HEAD)"
var (
	version = "dev"
	build   = "source"
)

const appHelp = `gorel — coordinated releases for multi-module Go repositories.

One shared version moves every module in the repo; a single module can take a
higher patch via --bump. The module prefix is auto-detected from go.mod, so the
same binary works in every repo, and re-runs only add the tags that are missing.

  gorel release v1.3.0               tag every module at v1.3.0
  gorel release v1.3.0 -b grpc=v1.3.1   bump grpc to v1.3.1, the rest at v1.3.0
  gorel list                         show each module's latest released version
  gorel repair                       repair go.sum against the published deps

Run it anywhere inside the target repository.`

func main() {
	cligo.Main(
		cligo.Name("gorel"),
		cligo.Title("gorel"),
		cligo.Version(version),
		cligo.Build(build),
		cligo.Help(appHelp),
		cligo.Beans(
			&ReleaseCmd{},
			&ListCmd{},
			&RepairCmd{},
		),
	)
}
