// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// canonicalHome resolves symlinks in a LIMA_HOME path.
//
// On macOS /tmp is a symlink to /private/tmp, and Lima reports the resolved
// path back. Canonicalising up front means test assertions compare like with
// like instead of hardcoding a platform quirk.
func canonicalHome(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

// TestMain sets up and tears down the isolated LIMA_HOME used by acceptance
// tests.
//
// It runs for unit tests too, but only creates a directory when TF_ACC is set,
// so a plain `go test ./...` touches nothing.
//
// It also hosts the `-sweep` entry point, which exits before any test runs. See
// sweep_test.go.
func TestMain(m *testing.M) {
	// Resolved before flag.Parse, and parsed here rather than left to m.Run,
	// because -sweep has to be readable before deciding whether to run tests at
	// all.
	sweep := sweepHomeFlag()
	flag.Parse()

	if home := strings.TrimSpace(sweep.Value.String()); home != "" {
		os.Exit(runSweep(home))
	}

	code := func() int {
		if os.Getenv("TF_ACC") == "" {
			return m.Run()
		}

		// An explicit home lets CI place the directory on a specific volume.
		if configured := os.Getenv(envAccHome); configured != "" {
			if err := os.MkdirAll(configured, 0o700); err != nil {
				fmt.Fprintf(os.Stderr, "creating acceptance LIMA_HOME %q: %v\n", configured, err)
				return 1
			}
			accHome = canonicalHome(configured)
			return m.Run()
		}

		// Short base path: Lima's unix socket paths must fit in 104 bytes.
		dir, err := os.MkdirTemp("/tmp", "ltfacc")
		if err != nil {
			fmt.Fprintf(os.Stderr, "creating acceptance LIMA_HOME: %v\n", err)
			return 1
		}
		accHome = canonicalHome(dir)
		defer func() {
			// Best effort: a leftover VM directory must not fail the run,
			// but it should be visible.
			if err := os.RemoveAll(dir); err != nil {
				fmt.Fprintf(os.Stderr, "removing acceptance LIMA_HOME %q: %v\n", dir, err)
			}
		}()

		fmt.Fprintf(os.Stderr, "acceptance tests using isolated LIMA_HOME %s\n", accHome)
		return m.Run()
	}()

	os.Exit(code)
}

// regexpMustCompile is a small helper so acceptance tests can express expected
// error patterns inline.
func regexpMustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
