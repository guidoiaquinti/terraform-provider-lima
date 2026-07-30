package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

// Create, Read, Update, Delete, ImportState and ValidateConfig for lima_disk.
//
// The interesting behaviour here is all in the refusals: a disk can grow but
// never shrink, and Lima locks one while its holder runs. Both produce a
// diagnostic that has to name the remedy, and neither was covered — the
// acceptance suite drives a real limactl, which does not fail on demand.

const testDiskName = "data"

func planForDisk(h *resourceHarness, name, size string, extra map[string]tftypes.Value) tftypes.Value {
	values := map[string]tftypes.Value{
		"name": tfString(name),
		"size": tfString(size),
	}
	for k, v := range extra {
		values[k] = v
	}
	return h.value(values)
}

func TestDiskCreateRecordsObservedState(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, diskResourceCtor)

	state, diags := h.create(planForDisk(h, testDiskName, "10GiB", map[string]tftypes.Value{
		"format": tfString("qcow2"),
	}))
	diags.requireNoError(t)

	got := getState[diskModel](t, state)
	if got.Name.ValueString() != testDiskName {
		t.Errorf("name = %q, want %q", got.Name.ValueString(), testDiskName)
	}
	if got.Dir.ValueString() == "" {
		t.Error("dir is empty after a successful create")
	}
	// A free disk is held by nobody, and that must read as null rather than "".
	if !got.InUseBy.IsNull() {
		t.Errorf("in_use_by = %q for a disk nobody holds, want null", got.InUseBy.ValueString())
	}
	if _, ok := fake.GetDisk(testDiskName); !ok {
		t.Error("no disk was created")
	}
}

// The configured spelling of a size is kept when it means the same byte count,
// so "10240MiB" does not fight with Lima reporting 10GiB.
func TestDiskCreateKeepsTheConfiguredSizeSpelling(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, diskResourceCtor)

	state, diags := h.create(planForDisk(h, testDiskName, "10240MiB", nil))
	diags.requireNoError(t)

	got := getState[diskModel](t, state)
	if got.Size.ValueString() != "10240MiB" {
		t.Errorf("size = %q, want the configured spelling 10240MiB — an equivalent respelling would show as a permanent diff",
			got.Size.ValueString())
	}
}

func TestDiskReadReflectsTheHoldingInstance(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: testDiskName, Size: 10 << 30, Format: "raw"})
	fake.Seed(testutil.FakeInstance{Name: "holder", Status: "Running"})
	fake.AttachDisk(testDiskName, "holder")
	h := newResourceHarness(t, fake, diskResourceCtor)

	state, diags := h.read(planForDisk(h, testDiskName, "10GiB", nil))
	diags.requireNoError(t)

	got := getState[diskModel](t, state)
	if got.InUseBy.ValueString() != "holder" {
		t.Errorf("in_use_by = %q, want holder", got.InUseBy.ValueString())
	}
}

func TestDiskReadRemovesAVanishedDiskFromState(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl() // nothing seeded
	h := newResourceHarness(t, fake, diskResourceCtor)

	state, diags := h.read(planForDisk(h, testDiskName, "10GiB", nil))
	diags.requireNoError(t)

	if !state.Raw.IsNull() {
		t.Error("state survived a read of a disk that no longer exists")
	}
}

func TestDiskUpdateGrowsInPlace(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: testDiskName, Size: 10 << 30, Format: "qcow2"})
	h := newResourceHarness(t, fake, diskResourceCtor)

	state := planForDisk(h, testDiskName, "10GiB", nil)
	plan := planForDisk(h, testDiskName, "20GiB", nil)

	newState, diags := h.update(state, plan)
	diags.requireNoError(t)

	got := getState[diskModel](t, newState)
	if got.Size.ValueString() != "20GiB" {
		t.Errorf("size = %q, want 20GiB", got.Size.ValueString())
	}
	if d, _ := fake.GetDisk(testDiskName); d.Size != 20<<30 {
		t.Errorf("the disk is %d bytes, want %d", d.Size, int64(20)<<30)
	}
}

// Lima cannot shrink a disk. The refusal has to happen before Lima is asked, so
// the diagnostic can name both sizes rather than relaying a generic failure.
func TestDiskUpdateRefusesToShrink(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: testDiskName, Size: 20 << 30, Format: "qcow2"})
	h := newResourceHarness(t, fake, diskResourceCtor)

	state := planForDisk(h, testDiskName, "20GiB", nil)
	plan := planForDisk(h, testDiskName, "10GiB", nil)

	_, diags := h.update(state, plan)
	diags.requireError(t, "shrink")

	if d, _ := fake.GetDisk(testDiskName); d.Size != 20<<30 {
		t.Errorf("the disk was resized to %d despite the refusal", d.Size)
	}
}

func TestDiskDeleteRemovesTheDisk(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: testDiskName, Size: 10 << 30, Format: "qcow2"})
	h := newResourceHarness(t, fake, diskResourceCtor)

	diags := h.delete(planForDisk(h, testDiskName, "10GiB", nil))
	diags.requireNoError(t)

	if _, ok := fake.GetDisk(testDiskName); ok {
		t.Error("the disk survived Delete")
	}
}

// `limactl disk unlock` exists but the provider deliberately never calls it: it
// cannot tell a stale lock from a live one. So the diagnostic has to hand the
// user the safe remedy instead.
func TestDiskDeleteRefusesWhileHeldAndNeverUnlocks(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: testDiskName, Size: 10 << 30, Format: "qcow2"})
	fake.Seed(testutil.FakeInstance{Name: "holder", Status: "Running"})
	fake.AttachDisk(testDiskName, "holder")
	h := newResourceHarness(t, fake, diskResourceCtor)

	diags := h.delete(planForDisk(h, testDiskName, "10GiB", nil))
	diags.requireError(t, "held by running instance")

	text := diags.text()
	if !strings.Contains(text, "holder") {
		t.Errorf("the diagnostic does not name the holding instance; got:\n%s", text)
	}
	if !strings.Contains(text, "limactl stop holder") {
		t.Errorf("the diagnostic does not give the remedy; got:\n%s", text)
	}
	if _, ok := fake.GetDisk(testDiskName); !ok {
		t.Error("a held disk was deleted anyway")
	}
	for _, call := range fake.Calls() {
		if call.Arg("unlock") {
			t.Error("the provider invoked `limactl disk unlock`, which it must never do")
		}
	}
}

func TestDiskImportRecordsWhatLimaReports(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: testDiskName, Size: 30 << 30, Format: "raw"})
	h := newResourceHarness(t, fake, diskResourceCtor)

	state, diags := h.importState(testDiskName)
	diags.requireNoError(t)

	got := getState[diskModel](t, state)
	if got.Name.ValueString() != testDiskName {
		t.Errorf("name = %q, want %q", got.Name.ValueString(), testDiskName)
	}
	if got.Size.ValueString() == "" {
		t.Error("size is empty after import; a matching configuration could not plan clean")
	}
}

func TestDiskImportRejectsUnknownAndEmptyIDs(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, diskResourceCtor)

	t.Run("unknown name", func(t *testing.T) {
		_, diags := h.importState("nope")
		diags.requireError(t, "not found")
	})

	t.Run("empty id", func(t *testing.T) {
		_, diags := h.importState("  ")
		diags.requireError(t, "import ID")
	})
}

// An unparseable size must fail at validate time, which is before a plan exists,
// rather than mid-apply.
func TestDiskValidateConfigRejectsAnUnparseableSize(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, diskResourceCtor)

	diags := h.validateConfig(planForDisk(h, testDiskName, "ten gigabytes", nil))
	diags.requireError(t, "size")
}

func TestDiskValidateConfigAcceptsAValidSizeAndAnAbsentOne(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, diskResourceCtor)

	t.Run("valid size", func(t *testing.T) {
		h.validateConfig(planForDisk(h, testDiskName, "10GiB", nil)).requireNoError(t)
	})

	// An unknown size — one coming from a variable or another resource — says
	// nothing about validity, so validation must skip rather than guess.
	t.Run("unknown size", func(t *testing.T) {
		config := h.value(map[string]tftypes.Value{
			"name": tfString(testDiskName),
			"size": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		})
		h.validateConfig(config).requireNoError(t)
	})
}
