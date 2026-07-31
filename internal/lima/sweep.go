// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package lima

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrSweepRefused reports that a sweep was declined before anything was
// removed.
//
// Distinct from a partial failure on purpose: a refusal means nothing was
// touched and retrying unchanged will refuse again, whereas a partial failure
// means the home is now closer to clean and a second pass may finish the job.
var ErrSweepRefused = errors.New("sweep refused")

// SweepOptions configures a sweep.
type SweepOptions struct {
	// Home is the LIMA_HOME being swept. It is not used to locate anything —
	// the client already knows where it is pointed — it exists so the sweep can
	// refuse to run against Lima's default home.
	//
	// An empty value means Lima's default and is therefore refused.
	Home string

	// AllowDefaultHome permits sweeping Lima's default home.
	//
	// Nothing in this repository sets it, and nothing should: it exists so that
	// the refusal is an explicit policy with a visible override rather than an
	// implicit limitation somebody removes while debugging. A caller that sets
	// this is asking to delete a developer's real virtual machines.
	AllowDefaultHome bool
}

// SweepReport records what a sweep did.
type SweepReport struct {
	// Instances and Disks name what was successfully removed.
	Instances []string
	Disks     []string
	// Failures records what could not be removed. A non-empty Failures always
	// accompanies a non-nil error from Sweep.
	Failures []error
}

// Sweep removes every instance and disk from the configured LIMA_HOME.
//
// It is the recovery path for an acceptance run that was interrupted before its
// cleanups ran — a Ctrl-C during `make testacc` skips every t.Cleanup and leaves
// real VMs behind. It is not used by the provider at runtime.
//
// Two properties matter more than tidiness:
//
// Order. Instances are removed before disks, because a running instance holds a
// lock on any disk attached to it and `limactl disk delete` fails while that
// lock is held. Sweeping disks first fails on exactly the disks that most need
// removing.
//
// Persistence. A failure does not stop the sweep. The caller is running this
// because state is already inconsistent, so stopping at the first problem
// leaves everything behind it — the opposite of what was asked for. Failures
// are collected and returned together.
func Sweep(ctx context.Context, svc *Service, disks *DiskService, opts SweepOptions) (SweepReport, error) {
	var report SweepReport

	if !opts.AllowDefaultHome {
		if err := refuseDefaultHome(opts.Home); err != nil {
			return report, err
		}
	}

	instances, err := svc.Client().List(ctx)
	if err != nil {
		return report, fmt.Errorf("listing instances to sweep: %w", err)
	}

	for _, inst := range instances {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		// Unprotect unconditionally rather than only when Protected is set.
		// The listing is a snapshot, `limactl unprotect` on an unprotected
		// instance is harmless, and a delete that fails on protection is the
		// single most likely way a sweep leaves a VM behind.
		if inst.Protected {
			if err := svc.SetProtection(ctx, inst.Name, false); err != nil {
				report.Failures = append(report.Failures,
					fmt.Errorf("unprotecting instance %q: %w", inst.Name, err))
				// Deleting will fail too, but attempt it: the protection may
				// have been cleared by something else since the listing.
			}
		}
		if err := svc.Delete(ctx, inst.Name); err != nil {
			report.Failures = append(report.Failures,
				fmt.Errorf("deleting instance %q: %w", inst.Name, err))
			continue
		}
		report.Instances = append(report.Instances, inst.Name)
	}

	// Disks are listed after the instances are gone, so a disk that was only
	// reachable once its holder was removed is still seen.
	found, err := disks.List(ctx)
	if err != nil {
		report.Failures = append(report.Failures, fmt.Errorf("listing disks to sweep: %w", err))
		return report, sweepError(report)
	}

	for _, d := range found {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := disks.Delete(ctx, d.Name); err != nil {
			report.Failures = append(report.Failures,
				fmt.Errorf("deleting disk %q: %w", d.Name, err))
			continue
		}
		report.Disks = append(report.Disks, d.Name)
	}

	return report, sweepError(report)
}

// refuseDefaultHome rejects a home that resolves to Lima's own default.
//
// ResolveHome turns an empty value into ~/.lima, which is what makes the
// no-argument case the dangerous one. Both sides are cleaned before comparison
// so that a trailing separator or a `..` segment cannot walk past the check.
func refuseDefaultHome(home string) error {
	resolved := filepath.Clean(ResolveHome(home))
	def := filepath.Clean(ResolveHome(""))

	if resolved == def {
		return fmt.Errorf("%w: %s is Lima's default LIMA_HOME, which holds real virtual machines; "+
			"point the sweep at the isolated home an acceptance run used", ErrSweepRefused, resolved)
	}
	return nil
}

// sweepError folds the collected failures into one error, or nil.
func sweepError(report SweepReport) error {
	if len(report.Failures) == 0 {
		return nil
	}
	parts := make([]string, 0, len(report.Failures))
	for _, f := range report.Failures {
		parts = append(parts, f.Error())
	}
	return fmt.Errorf("sweep completed with %d failure(s): %s",
		len(report.Failures), strings.Join(parts, "; "))
}
