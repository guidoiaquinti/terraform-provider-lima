// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// The `-sweep` entry point: recovery for an acceptance run that did not finish.
//
// `t.Cleanup` covers a test that fails, and the CI workflow covers a job that
// fails, but neither covers the case a developer actually hits — Ctrl-C during
// `make testacc`, which skips every registered cleanup and leaves running VMs
// plus a LIMA_HOME under /tmp. Recovering by hand needs the right limactl
// sequence in the right order, and the wrong order fails confusingly because a
// running instance holds its disks.
//
// This lives in the test binary rather than in the provider because it is a
// maintenance operation on test leftovers, not provider surface. The logic it
// calls, lima.Sweep, is unit-tested against the fake limactl in
// internal/lima/sweep_test.go; what is here is only argument handling.

// sweepTimeout bounds the whole sweep. Deleting a running VM is not instant, and
// a stuck delete must not hang a developer's terminal indefinitely. A constant
// rather than a flag: nothing about a cleanup run wants tuning, and every extra
// flag in a test binary is another chance to collide with one the testing
// framework already owns — see sweepHomeFlag.
const sweepTimeout = 10 * time.Minute

// sweepHomeFlag returns the `-sweep` flag, defining it only if nobody else has.
//
// terraform-plugin-testing registers `-sweep` itself (as "List of Regions to run
// available Sweepers") because this package imports helper/resource for the
// acceptance tests, and a second flag.String("sweep", ...) panics with "flag
// redefined" before any test runs. Reusing the flag rather than inventing
// `-sweep-home` also keeps the invocation conventional: `-sweep=<target>` is
// what every provider's sweeper takes, with LIMA_HOME as this provider's notion
// of a region.
//
// The framework's own sweeper registry is deliberately left unused: its
// AddTestSweepers/TestMain pair takes over process teardown, which would bypass
// the isolated-LIMA_HOME cleanup TestMain already owns.
func sweepHomeFlag() *flag.Flag {
	if f := flag.Lookup("sweep"); f != nil {
		return f
	}
	// Only reached if a future version of the testing framework stops defining
	// it. Declared before flag.Parse, so this is still legal here.
	flag.String("sweep", "",
		"remove every Lima instance and disk from this LIMA_HOME, then exit without running tests")
	return flag.Lookup("sweep")
}

// runSweep performs the sweep and returns a process exit code.
//
// Deliberately returns a code rather than calling os.Exit, so TestMain keeps
// ownership of process teardown in one place.
func runSweep(home string) int {
	ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
	defer cancel()

	client, err := lima.NewExecClient(lima.Options{Home: home})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sweep: %v\n", err)
		return 1
	}

	locks := lima.NewKeyedMutex()
	svc := lima.NewService(client, lima.WithLocks(locks))
	disks := lima.NewDiskService(client, locks)

	// Deliberately phrased as a target rather than an action: the safety check
	// inside Sweep may refuse, and announcing "sweeping X" before finding that
	// out reads like the deletion already started.
	fmt.Fprintf(os.Stderr, "sweep target: LIMA_HOME %s\n", home)

	// AllowDefaultHome is not plumbed to a flag on purpose. The refusal is the
	// point of the safety check, and a `--force` would be reached for by exactly
	// the person who should not.
	report, err := lima.Sweep(ctx, svc, disks, lima.SweepOptions{Home: home})

	for _, name := range report.Instances {
		fmt.Fprintf(os.Stderr, "  removed instance %s\n", name)
	}
	for _, name := range report.Disks {
		fmt.Fprintf(os.Stderr, "  removed disk %s\n", name)
	}

	switch {
	case errors.Is(err, lima.ErrSweepRefused):
		// No prefix: ErrSweepRefused already reads "sweep refused: ...", and
		// adding one produced "sweep refused: sweep refused: ...".
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	case err != nil:
		fmt.Fprintf(os.Stderr, "sweep incomplete: %v\n", err)
		return 1
	}

	if len(report.Instances) == 0 && len(report.Disks) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to sweep")
	}
	return 0
}
