package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

// Read for all four data sources.
//
// Data sources are the provider's read-only surface and had no unit coverage: a
// misnamed field or a wrongly-null value here is invisible until somebody's
// configuration references it and gets nothing back. Unlike the resources, a
// data source has no state to reconcile, so what matters is the mapping from
// Lima's JSON to the schema — which is exactly what the fake exercises.

func TestHostDataSourceReportsWhatLimaInfoSays(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "one", Status: "Running"})
	fake.Seed(testutil.FakeInstance{Name: "two", Status: "Stopped"})
	h := newDataSourceHarness(t, fake, hostDataSourceCtor)

	state, diags := h.read(h.value(nil))
	diags.requireNoError(t)

	got := getState[hostDataSourceModel](t, state)
	if got.LimaVersion.ValueString() != "2.2.0" {
		t.Errorf("lima_version = %q, want 2.2.0", got.LimaVersion.ValueString())
	}
	if got.HostOS.ValueString() != "darwin" {
		t.Errorf("host_os = %q, want darwin", got.HostOS.ValueString())
	}
	if got.BinaryPath.ValueString() == "" {
		t.Error("binary_path is empty; it identifies which limactl the provider is driving")
	}
	if got.Templates.IsNull() || len(got.Templates.Elements()) == 0 {
		t.Error("templates is empty")
	}
	if n := len(got.InstanceNames.Elements()); n != 2 {
		t.Errorf("instance_names has %d entries, want 2", n)
	}
}

// The schema deliberately excludes anything named like key material, and a test
// already enforces that for the resource. This is the data source half: Lima
// reports an identity file path in `limactl info`, and it must not surface.
func TestHostDataSourceExposesNoIdentityFile(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newDataSourceHarness(t, fake, hostDataSourceCtor)

	for name := range h.objectType().AttributeTypes {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "identity") || strings.Contains(lower, "private_key") {
			t.Errorf("lima_host exposes %q, which names key material", name)
		}
	}
}

func TestInstanceDataSourceReadsOneInstance(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{
		Name: "dev", Status: "Running", CPUs: 4, Arch: "aarch64", VMType: "vz", Protected: true,
	})
	h := newDataSourceHarness(t, fake, instanceDataSourceCtor)

	state, diags := h.read(h.value(map[string]tftypes.Value{
		"name": tfString("dev"),
	}))
	diags.requireNoError(t)

	got := getState[instanceDataSourceModel](t, state)
	if got.Status.ValueString() != "running" {
		t.Errorf("status = %q, want running", got.Status.ValueString())
	}
	if got.CPUs.ValueInt64() != 4 {
		t.Errorf("cpus = %d, want 4", got.CPUs.ValueInt64())
	}
	if !got.Protected.ValueBool() {
		t.Error("protected = false for a protected instance")
	}
	if got.Hostname.ValueString() != "lima-dev" {
		t.Errorf("hostname = %q, want lima-dev", got.Hostname.ValueString())
	}
}

// A data source naming something that does not exist is a configuration error,
// not silent emptiness — and the diagnostic has to say which home was searched,
// because looking in the wrong LIMA_HOME is the usual cause.
func TestInstanceDataSourceReportsAMissingInstance(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newDataSourceHarness(t, fake, instanceDataSourceCtor, func(d *providerData) {
		d.Home = "/tmp/somewhere"
	})

	_, diags := h.read(h.value(map[string]tftypes.Value{
		"name": tfString("nope"),
	}))
	diags.requireError(t, "nope")

	if !strings.Contains(diags.text(), "/tmp/somewhere") {
		t.Errorf("the diagnostic does not say which LIMA_HOME was searched; got:\n%s", diags.text())
	}
}

func TestInstancesDataSourceListsEveryInstance(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "alpha", Status: "Running"})
	fake.Seed(testutil.FakeInstance{Name: "beta", Status: "Stopped"})
	fake.Seed(testutil.FakeInstance{Name: "gamma", Status: "Broken"})
	h := newDataSourceHarness(t, fake, instancesDataSourceCtor)

	state, diags := h.read(h.value(nil))
	diags.requireNoError(t)

	got := getState[instancesDataSourceModel](t, state)
	if len(got.Instances) != 3 {
		t.Fatalf("got %d instances, want 3", len(got.Instances))
	}

	byName := map[string]instanceEntryModel{}
	for _, entry := range got.Instances {
		byName[entry.Name.ValueString()] = entry
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("instance %q is missing from the list", want)
		}
	}
	if byName["alpha"].Status.ValueString() != "running" {
		t.Errorf("alpha status = %q, want running", byName["alpha"].Status.ValueString())
	}
	// An unrecognised Lima status must arrive as `unknown` with the original
	// preserved, rather than being dropped or guessed at.
	if got := byName["gamma"].Status.ValueString(); got != "broken" {
		t.Errorf("gamma status = %q, want broken", got)
	}
}

// An empty home is a legitimate answer, not an error: a configuration filtering
// the list has to be able to get zero results.
func TestInstancesDataSourceOnAnEmptyHomeReturnsAnEmptyList(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newDataSourceHarness(t, fake, instancesDataSourceCtor)

	state, diags := h.read(h.value(nil))
	diags.requireNoError(t)

	got := getState[instancesDataSourceModel](t, state)
	if len(got.Instances) != 0 {
		t.Errorf("got %d instances for an empty home, want 0", len(got.Instances))
	}
}

func TestDiskDataSourceReadsOneDisk(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.SeedDisk(testutil.FakeDisk{Name: "data", Size: 25 << 30, Format: "raw"})
	fake.Seed(testutil.FakeInstance{Name: "holder", Status: "Running"})
	fake.AttachDisk("data", "holder")
	h := newDataSourceHarness(t, fake, diskDataSourceCtor)

	state, diags := h.read(h.value(map[string]tftypes.Value{
		"name": tfString("data"),
	}))
	diags.requireNoError(t)

	got := getState[diskDataSourceModel](t, state)
	if got.Name.ValueString() != "data" {
		t.Errorf("name = %q, want data", got.Name.ValueString())
	}
	if got.InUseBy.ValueString() != "holder" {
		t.Errorf("in_use_by = %q, want holder", got.InUseBy.ValueString())
	}
	if got.Dir.ValueString() == "" {
		t.Error("dir is empty")
	}
}

func TestDiskDataSourceReportsAMissingDisk(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newDataSourceHarness(t, fake, diskDataSourceCtor)

	_, diags := h.read(h.value(map[string]tftypes.Value{
		"name": tfString("nope"),
	}))
	diags.requireError(t, "nope")
}
