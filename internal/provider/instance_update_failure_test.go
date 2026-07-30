package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

// An update can fail after it has already changed something. Those paths were the
// last correctness findings without automated coverage, because provoking them
// needs Lima to fail on demand — the acceptance suite drives the real binary,
// which succeeds.
//
// The fake limactl already injects failures per command and per instance, so the
// resource's Update can be driven directly against it. That exercises the real
// Update, with the real Service and the real argument construction, rather than
// an extracted fragment of the logic.
//
// The shared harness in harness_test.go initialises the response state to null,
// which is what the framework does. That matters here: these tests ask whether
// Update *recorded* what it did, and a harness that pre-seeded the prior state
// into the response would answer yes without the resource writing anything.

// Protection is cleared before the rest of an update and reapplied afterwards, so
// that a protected instance can be reconfigured in one apply. A failure in between
// used to return without writing state, leaving `protect = true` recorded against
// an instance that had just been unprotected.
func TestUpdateRecordsProtectionAfterAFailedResize(t *testing.T) {
	t.Parallel()

	const name = "dev"
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: name, Status: "Stopped", CPUs: 2, Protected: true})
	// The edit fails, after protection has already been cleared.
	fake.EditFailsFor = map[string]string{name: "cannot edit for reasons"}

	h := newResourceHarness(t, fake, instanceResourceCtor)

	state := h.value(map[string]tftypes.Value{
		"name":          tfString(name),
		"instance_name": tfString(name),
		"cpus":          tfNumber(2),
		"protect":       tfBool(true),
		"start":         tfBool(false),
	})
	plan := h.value(map[string]tftypes.Value{
		"name":          tfString(name),
		"instance_name": tfString(name),
		"cpus":          tfNumber(4),
		"protect":       tfBool(false),
		"start":         tfBool(false),
	})

	newState, diags := h.update(state, plan)
	if !diags.HasError() {
		t.Fatal("a failed resize produced no error diagnostic")
	}

	got := getState[instanceModel](t, newState)
	// Lima really is unprotected now, so state must not claim otherwise.
	if got.Protect.ValueBool() {
		t.Errorf("state records protect = true after protection was cleared and the resize failed; diagnostics: %v",
			diags.summaries())
	}
}

// A restart failure after a successful edit is not a failed change: the resources
// were applied and only bringing the instance back up did not work. Keeping the
// old values in state would make the next plan propose a change that has already
// happened.
func TestUpdateRecordsAppliedResourcesWhenTheRestartFails(t *testing.T) {
	t.Parallel()

	const name = "dev"
	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: name, Status: "Running", CPUs: 2})
	// The edit succeeds; the start afterwards does not.
	fake.StartFailsFor = map[string]string{name: "could not boot"}

	h := newResourceHarness(t, fake, instanceResourceCtor)

	state := h.value(map[string]tftypes.Value{
		"name":          tfString(name),
		"instance_name": tfString(name),
		"cpus":          tfNumber(2),
		"start":         tfBool(true),
		"protect":       tfBool(false),
	})
	plan := h.value(map[string]tftypes.Value{
		"name":          tfString(name),
		"instance_name": tfString(name),
		"cpus":          tfNumber(4),
		"start":         tfBool(true),
		"protect":       tfBool(false),
	})

	newState, diags := h.update(state, plan)
	if !diags.HasError() {
		t.Fatal("a failed restart produced no error diagnostic")
	}

	got := getState[instanceModel](t, newState)
	// The edit applied, so the new CPU count is the truth.
	if got.CPUs.ValueInt64() != 4 {
		t.Errorf("state records cpus = %d after a successful edit; want 4, so the next plan does not re-apply it. diagnostics: %v",
			got.CPUs.ValueInt64(), diags.summaries())
	}
	// And the instance is down, which is the thing the user has to act on.
	if got.Start.ValueBool() {
		t.Errorf("state records start = true after the restart failed; diagnostics: %v", diags.summaries())
	}
}
