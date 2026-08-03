// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// StubBinary writes an executable no-op script and returns its path.
//
// Tests that inject a FakeLimactl runner still need a real file on disk,
// because the adapter validates the configured binary before ever running it.
// Using a stub keeps that validation honest without depending on any
// particular system binary being present.
func StubBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "limactl")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub binary: %v", err)
	}
	return path
}

// ShortHome returns a short temporary LIMA_HOME.
//
// Lima builds unix socket paths as <LIMA_HOME>/<name>/ssh.sock.<16 digits> and
// enforces UNIX_PATH_MAX=104, so the deep paths t.TempDir() produces on macOS
// would make instance creation fail. Acceptance tests need a short base.
func ShortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ltf") //nolint:usetesting // t.TempDir is too long for Lima's UNIX socket path
	if err != nil {
		t.Fatalf("creating short LIMA_HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
