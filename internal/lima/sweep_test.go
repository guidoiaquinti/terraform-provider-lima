// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

// Sweeping is the recovery path for an acceptance run that did not finish.
//
// t.Cleanup and the shell loop in CI both handle the ordinary case. Neither
// handles the case that actually happens to a developer: Ctrl-C during
// `make testacc`, which skips every registered cleanup and leaves real VMs
// running plus a LIMA_HOME under /tmp. Before this existed the only remedy was
// to remember the right sequence of limactl commands by hand, in the right
// order, and the wrong order fails confusingly.
//
// The safety property is the reason this is a function with tests rather than a
// shell one-liner: a sweeper that can reach `~/.lima` is a tool for destroying
// somebody's actual development environment.

func newSweepServices(t *testing.T, fake *testutil.FakeLimactl) (*lima.Service, *lima.DiskService) {
	t.Helper()
	fake.ReadFile = os.ReadFile
	c, err := lima.NewExecClient(lima.Options{Binary: testutil.StubBinary(t), Runner: fake})
	if err != nil {
		t.Fatalf("NewExecClient: %v", err)
	}
	// One KeyedMutex shared between the two services, exactly as the provider
	// wires them, so an instance delete and a disk delete cannot interleave
	// differently here than in production.
	locks := lima.NewKeyedMutex()
	svc := lima.NewService(c, lima.WithPollOptions(fastPoll), lima.WithLocks(locks))
	return svc, lima.NewDiskService(c, locks)
}

// The central safety property. Everything else in this file is secondary to it.
func TestSweepRefusesLimasDefaultHome(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %v", err)
	}

	for _, tc := range []struct {
		name string
		home string
	}{
		// An unset home means Lima's default, which is the dangerous case: a
		// sweeper invoked with no argument must not quietly target ~/.lima.
		{"empty means the default", ""},
		{"the default spelled out", filepath.Join(home, lima.DefaultHomeDir)},
		// A trailing separator is the same directory and must not slip through
		// a string comparison.
		{"the default with a trailing slash", filepath.Join(home, lima.DefaultHomeDir) + string(os.PathSeparator)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := testutil.NewFakeLimactl()
			fake.Seed(testutil.FakeInstance{Name: "precious", Status: "Running"})
			svc, disks := newSweepServices(t, fake)

			_, err := lima.Sweep(context.Background(), svc, disks, lima.SweepOptions{Home: tc.home})
			if err == nil {
				t.Fatal("Sweep accepted Lima's default home; it must refuse")
			}
			if !errors.Is(err, lima.ErrSweepRefused) {
				t.Errorf("error = %v, want it to wrap ErrSweepRefused", err)
			}
			// Refusing means refusing before doing anything, not after.
			if n := len(fake.CallsFor("delete")); n != 0 {
				t.Errorf("delete was invoked %d times despite the refusal", n)
			}
			if _, ok := fake.Get("precious"); !ok {
				t.Error("the seeded instance was removed despite the refusal")
			}
		})
	}
}

func TestSweepRemovesInstancesAndDisks(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "acc-one", Status: "Running"})
	fake.Seed(testutil.FakeInstance{Name: "acc-two", Status: "Stopped"})
	fake.SeedDisk(testutil.FakeDisk{Name: "acc-data", Size: 1 << 30, Format: "qcow2"})
	svc, disks := newSweepServices(t, fake)

	report, err := lima.Sweep(context.Background(), svc, disks, lima.SweepOptions{Home: "/tmp/ltfacc-xyz"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if got := fake.Instances(); len(got) != 0 {
		t.Errorf("instances remain after the sweep: %v", got)
	}
	if _, ok := fake.GetDisk("acc-data"); ok {
		t.Error("disk acc-data remains after the sweep")
	}
	if len(report.Instances) != 2 {
		t.Errorf("report lists %v, want both instances", report.Instances)
	}
	if len(report.Disks) != 1 {
		t.Errorf("report lists disks %v, want one", report.Disks)
	}
}

// A protected instance is exactly what a killed run leaves behind when it was
// testing protection, and `limactl delete` refuses one. Unprotecting is
// therefore part of sweeping, not something the caller should have to know.
func TestSweepUnprotectsBeforeDeleting(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "acc-locked", Status: "Stopped", Protected: true})
	svc, disks := newSweepServices(t, fake)

	if _, err := lima.Sweep(context.Background(), svc, disks, lima.SweepOptions{Home: "/tmp/ltfacc-xyz"}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if _, ok := fake.Get("acc-locked"); ok {
		t.Error("the protected instance survived the sweep")
	}
	if n := len(fake.CallsFor("unprotect")); n == 0 {
		t.Error("unprotect was never invoked for a protected instance")
	}
}

// Ordering is a real constraint, not tidiness: a running instance holds a lock
// on any disk attached to it, so a disk-first sweep fails on exactly the disks
// that most need removing.
func TestSweepRemovesInstancesBeforeDisks(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "acc-holder", Status: "Running"})
	fake.SeedDisk(testutil.FakeDisk{Name: "acc-held", Size: 1 << 30, Format: "raw"})
	fake.AttachDisk("acc-held", "acc-holder")
	svc, disks := newSweepServices(t, fake)

	if _, err := lima.Sweep(context.Background(), svc, disks, lima.SweepOptions{Home: "/tmp/ltfacc-xyz"}); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// Find the first instance delete and the first disk delete in the log.
	instanceDelete, diskDelete := -1, -1
	for i, call := range fake.Calls() {
		isDisk := call.Arg("disk")
		switch {
		case call.Command() == "disk" && isDisk && call.Arg("delete") && diskDelete < 0:
			diskDelete = i
		case call.Command() == "delete" && !isDisk && instanceDelete < 0:
			instanceDelete = i
		}
	}
	if instanceDelete < 0 {
		t.Fatalf("no instance delete in the call log: %v", fake.Calls())
	}
	if diskDelete < 0 {
		t.Fatalf("no disk delete in the call log: %v", fake.Calls())
	}
	if instanceDelete > diskDelete {
		t.Errorf("disk was deleted (call %d) before the instance holding it (call %d)", diskDelete, instanceDelete)
	}
}

// A sweeper that stops at the first failure leaves everything after it behind,
// which defeats the purpose: the caller runs it precisely because state is
// already messy. It must keep going and report.
func TestSweepContinuesAfterAFailureAndReportsIt(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "acc-bad", Status: "Stopped"})
	fake.Seed(testutil.FakeInstance{Name: "acc-good", Status: "Stopped"})
	fake.SeedDisk(testutil.FakeDisk{Name: "acc-data", Size: 1 << 30, Format: "qcow2"})
	fake.Script(testutil.Scripted{
		Command:  "delete",
		Name:     "acc-bad",
		Stderr:   "cannot delete for reasons",
		ExitCode: 1,
	})
	svc, disks := newSweepServices(t, fake)

	report, err := lima.Sweep(context.Background(), svc, disks, lima.SweepOptions{Home: "/tmp/ltfacc-xyz"})
	if err == nil {
		t.Fatal("Sweep reported success despite a failed delete")
	}
	// Refusal and partial failure are different outcomes; a caller distinguishes
	// them to decide whether retrying could help.
	if errors.Is(err, lima.ErrSweepRefused) {
		t.Error("a failed delete was reported as a refusal")
	}
	if !strings.Contains(err.Error(), "acc-bad") {
		t.Errorf("error %q does not name the instance that failed", err)
	}
	if _, ok := fake.Get("acc-good"); ok {
		t.Error("acc-good survived; the sweep stopped at the first failure")
	}
	if _, ok := fake.GetDisk("acc-data"); ok {
		t.Error("the disk survived; the sweep stopped before reaching disks")
	}
	if len(report.Failures) == 0 {
		t.Error("the report records no failures")
	}
}

// The reason the ordering above works: Lima's disk lock belongs to a *running*
// holder, so removing the instance releases it. This pins that rule directly,
// because the ordering test would otherwise pass for the wrong reason if the
// lock were never modelled at all.
func TestSweepReleasesADiskLockByRemovingItsHolder(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "acc-holder", Status: "Running"})
	fake.SeedDisk(testutil.FakeDisk{Name: "acc-held", Size: 1 << 30, Format: "raw"})
	fake.AttachDisk("acc-held", "acc-holder")
	svc, disks := newSweepServices(t, fake)

	// Before: the disk is genuinely locked, so a disk-first sweep would fail.
	if err := disks.Delete(context.Background(), "acc-held"); !errors.Is(err, lima.ErrDiskInUse) {
		t.Fatalf("deleting a held disk returned %v, want ErrDiskInUse — the lock is not being modelled", err)
	}

	report, err := lima.Sweep(context.Background(), svc, disks, lima.SweepOptions{Home: "/tmp/ltfacc-xyz"})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(report.Disks) != 1 {
		t.Errorf("report lists disks %v, want the previously held disk", report.Disks)
	}
	if _, ok := fake.GetDisk("acc-held"); ok {
		t.Error("the held disk survived a sweep that removed its holder first")
	}
}

// Sweeping an already-clean home is the common case when someone runs the
// target defensively. It must be a no-op, not an error.
func TestSweepOfACleanHomeSucceeds(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	svc, disks := newSweepServices(t, fake)

	report, err := lima.Sweep(context.Background(), svc, disks, lima.SweepOptions{Home: "/tmp/ltfacc-xyz"})
	if err != nil {
		t.Fatalf("Sweep of a clean home: %v", err)
	}
	if len(report.Instances) != 0 || len(report.Disks) != 0 {
		t.Errorf("report = %+v, want nothing swept", report)
	}
}
