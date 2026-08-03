// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

// Package main is the provider's plugin entry point. Terraform executes this
// binary and speaks the plugin protocol to it over a handshake on stdio; all of
// the provider's behaviour lives in internal/provider.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/provider"
)

// version is overridden at build time by GoReleaser via -ldflags. The default
// is the version being developed towards, with a -dev suffix so a local build
// can never be mistaken for a release. Bump it after each tag, in step with
// the Makefile's VERSION; docs/development/releasing.md lists this as a step.
var version = "0.1.1-dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false,
		"run the provider with support for debuggers such as delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/guidoiaquinti/lima",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}
