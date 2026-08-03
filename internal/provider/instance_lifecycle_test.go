// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"maps"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/testutil"
)

// Create, Read, Delete and ImportState for lima_instance, driven against the
// fake limactl.
//
// The acceptance suite already proves these work against real VMs. What it
// cannot do is make Lima fail, so every diagnostic the provider emits on a
// failure path — the import instruction after a name collision, the refusal to
// unprotect, the leftover-instance warning — shipped without a single test. A
// diagnostic is the whole user interface of a failed apply, and these were the
// least verified part of the provider.

const testInstanceName = "dev"

// planForCreate is the minimal plan a create needs: a name, a template, and the
// two booleans the resource reads unconditionally.
func planForCreate(h *resourceHarness, name string, extra map[string]tftypes.Value) tftypes.Value {
	values := map[string]tftypes.Value{
		"name":     tfString(name),
		"template": tfString("template:alpine"),
		"start":    tfBool(false),
		"protect":  tfBool(false),
	}
	maps.Copy(values, extra)
	return h.value(values)
}

func TestInstanceCreateRecordsObservedState(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	state, diags := h.create(planForCreate(h, testInstanceName, nil))
	diags.requireNoError(t)

	got := getState[instanceModel](t, state)
	if got.InstanceName.ValueString() != testInstanceName {
		t.Errorf("instance_name = %q, want %q", got.InstanceName.ValueString(), testInstanceName)
	}
	if got.Status.ValueString() != "stopped" {
		t.Errorf("status = %q, want stopped", got.Status.ValueString())
	}
	// config_hash is derived from the rendered document, not read back from
	// Lima, so it must be populated even for a stopped instance.
	if got.ConfigHash.ValueString() == "" {
		t.Error("config_hash is empty after a successful create")
	}
	if _, ok := fake.Get(testInstanceName); !ok {
		t.Error("no instance was created")
	}
}

// name_prefix is applied on the way to Lima and stripped on the way back. A
// create must use the prefixed name for the VM while `name` stays logical.
func TestInstanceCreateAppliesTheNamePrefix(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor, func(d *providerData) {
		d.NamePrefix = "acme-"
	})

	state, diags := h.create(planForCreate(h, testInstanceName, nil))
	diags.requireNoError(t)

	got := getState[instanceModel](t, state)
	if got.Name.ValueString() != testInstanceName {
		t.Errorf("name = %q, want the logical name %q", got.Name.ValueString(), testInstanceName)
	}
	if want := "acme-" + testInstanceName; got.InstanceName.ValueString() != want {
		t.Errorf("instance_name = %q, want %q", got.InstanceName.ValueString(), want)
	}
	if _, ok := fake.Get("acme-" + testInstanceName); !ok {
		t.Errorf("no instance named acme-%s was created; got %v", testInstanceName, fake.Instances())
	}
}

// A name collision is the one create failure with a specific remedy, and the
// diagnostic has to carry it. The provider previously reported this for a
// completely unrelated failure, because it matched a bare "already exists"
// substring — see the CHANGELOG.
func TestInstanceCreateOnCollisionAdvisesImport(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: testInstanceName, Status: "Running"})
	h := newResourceHarness(t, fake, instanceResourceCtor)

	_, diags := h.create(planForCreate(h, testInstanceName, nil))
	diags.requireError(t, "already exists")

	if !strings.Contains(diags.text(), "terraform import lima_instance.example "+testInstanceName) {
		t.Errorf("the collision diagnostic does not give a runnable import command; got:\n%s", diags.text())
	}
}

// Lima can register an instance and then fail to start it. The provider
// deliberately does not delete the leftover, so state must record it or the VM
// is orphaned outside Terraform's knowledge, and the diagnostic must say so.
func TestInstanceCreateRecordsAPartiallyCreatedInstance(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.CreateFailsFor = map[string]string{testInstanceName: "could not provision the VM"}
	fake.CreateRegistersOnFailure = true
	h := newResourceHarness(t, fake, instanceResourceCtor)

	state, diags := h.create(planForCreate(h, testInstanceName, nil))
	diags.requireError(t, "could not provision the VM")

	// State is written despite the failure: this is the property that keeps the
	// leftover destroyable.
	got := getState[instanceModel](t, state)
	if got.InstanceName.ValueString() != testInstanceName {
		t.Errorf("instance_name = %q; a registered-but-failed instance must be recorded so destroy can remove it",
			got.InstanceName.ValueString())
	}
	if !strings.Contains(diags.text(), "limactl delete --force "+testInstanceName) {
		t.Errorf("the diagnostic does not explain how to remove the leftover; got:\n%s", diags.text())
	}
}

// A create that fails without registering anything must not write state. Writing
// it would leave Terraform managing a VM that does not exist.
func TestInstanceCreateWritesNoStateWhenNothingWasRegistered(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.CreateFailsFor = map[string]string{testInstanceName: "refused before registering"}
	fake.CreateRegistersOnFailure = false
	h := newResourceHarness(t, fake, instanceResourceCtor)

	state, diags := h.create(planForCreate(h, testInstanceName, nil))
	diags.requireError(t, "refused before registering")

	if !state.Raw.IsNull() {
		got := getState[instanceModel](t, state)
		t.Errorf("state was written for an instance that was never registered: instance_name = %q",
			got.InstanceName.ValueString())
	}
}

func TestInstanceReadReflectsObservedStatus(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{
		Name: testInstanceName, Status: "Running", CPUs: 4, Arch: "x86_64", VMType: "qemu",
	})
	h := newResourceHarness(t, fake, instanceResourceCtor)

	state, diags := h.read(h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
		"start":         tfBool(true),
		"protect":       tfBool(false),
	}))
	diags.requireNoError(t)

	got := getState[instanceModel](t, state)
	if got.Status.ValueString() != "running" {
		t.Errorf("status = %q, want running", got.Status.ValueString())
	}
	if got.Hostname.ValueString() != "lima-"+testInstanceName {
		t.Errorf("hostname = %q, want lima-%s", got.Hostname.ValueString(), testInstanceName)
	}
}

// Reading Lima's resolved configuration back is safe only where the user
// actually set the attribute. `cpus` is Optional and not Computed, so an
// unconfigured value must stay null: writing Lima's resolved default into it
// would put a permanent diff in every subsequent plan.
func TestInstanceReadLeavesAnUnsetSizingAttributeNull(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	// The fake resolves an unset CPU count to 4, exactly as Lima resolves a
	// template default — so if the provider read it back, it would land here.
	fake.Seed(testutil.FakeInstance{Name: testInstanceName, Status: "Running"})
	h := newResourceHarness(t, fake, instanceResourceCtor)

	state, diags := h.read(h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
		"start":         tfBool(true),
		"protect":       tfBool(false),
		// cpus deliberately absent from state.
	}))
	diags.requireNoError(t)

	got := getState[instanceModel](t, state)
	if !got.CPUs.IsNull() {
		t.Errorf("cpus = %d for an unconfigured attribute; Lima's resolved default must not be written into state",
			got.CPUs.ValueInt64())
	}
}

// The other half of the same rule: a value the user *did* pin is drift-checked,
// so someone editing the VM behind Terraform's back shows up in the next plan.
func TestInstanceReadSurfacesDriftInAConfiguredSizingAttribute(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: testInstanceName, Status: "Running", CPUs: 8})
	h := newResourceHarness(t, fake, instanceResourceCtor)

	state, diags := h.read(h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
		"start":         tfBool(true),
		"protect":       tfBool(false),
		"cpus":          tfNumber(2), // what Terraform last recorded
	}))
	diags.requireNoError(t)

	got := getState[instanceModel](t, state)
	if got.CPUs.ValueInt64() != 8 {
		t.Errorf("cpus = %d, want 8 — an externally changed CPU count must reach state to show as drift",
			got.CPUs.ValueInt64())
	}
}

// An instance deleted outside Terraform must drop out of state so the next plan
// proposes recreating it, rather than failing forever on a missing VM.
func TestInstanceReadRemovesAVanishedInstanceFromState(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl() // nothing seeded: the instance is gone
	h := newResourceHarness(t, fake, instanceResourceCtor)

	state, diags := h.read(h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
		"start":         tfBool(true),
		"protect":       tfBool(false),
	}))
	diags.requireNoError(t)

	if !state.Raw.IsNull() {
		t.Error("state survived a read of an instance that no longer exists; the next plan cannot recreate it")
	}
}

// `start` is desired state, and a status that settles nothing must not overwrite
// it. Deriving it from `status == "running"` made a Broken or half-created
// instance read as "stopped" and produced a plan proposing a start nobody asked
// for.
func TestInstanceReadLeavesStartAloneForAnInconclusiveStatus(t *testing.T) {
	t.Parallel()

	// Only statuses the fake will preserve: it resolves an empty status to
	// "Stopped", exactly as a seeded instance would report, so "" cannot be
	// expressed here. StatusUnknown is reached through an unrecognised value
	// instead, which is also how a future Lima status would arrive.
	for _, status := range []string{"Broken", "Installing", "Uninitialized", "Hibernating"} {
		t.Run("status "+status, func(t *testing.T) {
			t.Parallel()

			fake := testutil.NewFakeLimactl()
			fake.Seed(testutil.FakeInstance{Name: testInstanceName, Status: status})
			h := newResourceHarness(t, fake, instanceResourceCtor)

			state, diags := h.read(h.value(map[string]tftypes.Value{
				"name":          tfString(testInstanceName),
				"instance_name": tfString(testInstanceName),
				"start":         tfBool(true),
				"protect":       tfBool(false),
			}))
			diags.requireNoError(t)

			got := getState[instanceModel](t, state)
			if !got.Start.ValueBool() {
				t.Errorf("status %q overwrote start = true with false; only running and stopped may settle it", status)
			}
		})
	}
}

func TestInstanceDeleteRemovesTheInstance(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: testInstanceName, Status: "Stopped"})
	h := newResourceHarness(t, fake, instanceResourceCtor)

	diags := h.delete(h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
	}))
	diags.requireNoError(t)

	if _, ok := fake.Get(testInstanceName); ok {
		t.Error("the instance survived Delete")
	}
}

// Protection exists to stop an accidental destroy, so the provider must not work
// around it. The diagnostic has to offer both remedies, because one of them
// (`protect = false` then apply) keeps the operation inside Terraform.
func TestInstanceDeleteRefusesToUnprotect(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: testInstanceName, Status: "Stopped", Protected: true})
	h := newResourceHarness(t, fake, instanceResourceCtor)

	diags := h.delete(h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
	}))
	diags.requireError(t, "protected")

	text := diags.text()
	if !strings.Contains(text, "protect = false") {
		t.Errorf("the diagnostic does not offer the in-Terraform remedy; got:\n%s", text)
	}
	if !strings.Contains(text, "limactl unprotect "+testInstanceName) {
		t.Errorf("the diagnostic does not offer the manual remedy; got:\n%s", text)
	}
	if _, ok := fake.Get(testInstanceName); !ok {
		t.Error("the protected instance was deleted anyway")
	}
	for _, call := range fake.Calls() {
		if call.Command() == "unprotect" {
			t.Error("the provider silently removed the user's deletion protection")
		}
	}
}

func TestInstanceImportRecordsWhatLimaReports(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{
		Name: "project-dev", Status: "Running", CPUs: 8, Arch: "aarch64", VMType: "vz", Protected: true,
	})
	h := newResourceHarness(t, fake, instanceResourceCtor)

	state, diags := h.importState("project-dev")
	diags.requireNoError(t)

	got := getState[instanceModel](t, state)
	if got.InstanceName.ValueString() != "project-dev" {
		t.Errorf("instance_name = %q, want project-dev", got.InstanceName.ValueString())
	}
	if got.CPUs.ValueInt64() != 8 {
		t.Errorf("cpus = %d, want 8 — import must record what Lima reports so a matching config plans clean",
			got.CPUs.ValueInt64())
	}
	if !got.Start.ValueBool() {
		t.Error("start = false for a running instance")
	}
	if !got.Protect.ValueBool() {
		t.Error("protect = false for a protected instance")
	}
	// Lima does not record which template an instance came from, so declaring
	// one afterwards adopts rather than recreates. Import must therefore leave
	// it unset rather than guess.
	if !got.Template.IsNull() {
		t.Errorf("template = %q after import; Lima does not record it, so it must stay null",
			got.Template.ValueString())
	}
}

// The import ID is the real Lima name including any prefix, and `name` is
// derived from it. Importing `acme-dev` under prefix `acme-` must yield
// name = "dev", which re-derives to `acme-dev` — no double prefixing.
func TestInstanceImportStripsTheNamePrefix(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{Name: "acme-dev", Status: "Stopped"})
	h := newResourceHarness(t, fake, instanceResourceCtor, func(d *providerData) {
		d.NamePrefix = "acme-"
	})

	state, diags := h.importState("acme-dev")
	diags.requireNoError(t)

	got := getState[instanceModel](t, state)
	if got.Name.ValueString() != "dev" {
		t.Errorf("name = %q, want dev", got.Name.ValueString())
	}
	if got.InstanceName.ValueString() != "acme-dev" {
		t.Errorf("instance_name = %q, want acme-dev", got.InstanceName.ValueString())
	}
}

func TestInstanceImportRejectsAnUnknownName(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	_, diags := h.importState("nope")
	diags.requireError(t, "not found")

	// The remedy is to list what does exist.
	if !strings.Contains(diags.text(), "limactl list") {
		t.Errorf("the diagnostic does not tell the user how to find the right name; got:\n%s", diags.text())
	}
}

func TestInstanceImportRejectsAnEmptyID(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	for _, id := range []string{"", "   "} {
		_, diags := h.importState(id)
		diags.requireError(t, "import ID")
	}
}

// ValidateConfig is the framework wiring around validateInstanceConfig, which is
// already unit-tested on its own. The wiring is worth covering separately
// because it is where two shipped bugs lived: decoding the configuration into Go
// slices made every `dynamic` block fail with a Value Conversion Error before
// planning began, and the instance-name length check returned early whenever no
// `home` was configured — that is, for the default installation.
func TestInstanceValidateConfigAcceptsAWellFormedConfiguration(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	h.validateConfig(planForCreate(h, testInstanceName, nil)).requireNoError(t)
}

func TestInstanceValidateConfigRequiresAnInstanceSource(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	// Neither template nor config, and no typed attribute either: Lima would
	// have no image at all, which fails confusingly inside Lima.
	diags := h.validateConfig(h.value(map[string]tftypes.Value{
		"name": tfString(testInstanceName),
	}))
	diags.requireError(t, "Missing instance source")
	if !strings.Contains(diags.text(), "--list-templates") {
		t.Errorf("the diagnostic does not tell the user how to find a template; got:\n%s", diags.text())
	}
}

// With typed attributes but no source, the configuration is usable — it just
// leans entirely on Lima's defaults — so this warns rather than fails.
func TestInstanceValidateConfigWarnsWhenOnlyTypedAttributesAreSet(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	diags := h.validateConfig(h.value(map[string]tftypes.Value{
		"name": tfString(testInstanceName),
		"cpus": tfNumber(4),
	}))
	diags.requireNoError(t)
	if !diags.hasWarning("No template or config set") {
		t.Errorf("a source-less configuration produced no warning; got:\n%s", diags.text())
	}
}

// An unknown source says nothing about whether one was provided, so the check
// must be skipped rather than guessed at. Warning here fired on every plan whose
// `config` came from a variable or another resource.
func TestInstanceValidateConfigStaysSilentForAnUnknownSource(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	for _, attr := range []string{"template", "config"} {
		t.Run(attr+" is unknown", func(t *testing.T) {
			t.Parallel()

			diags := h.validateConfig(h.value(map[string]tftypes.Value{
				"name": tfString(testInstanceName),
				attr:   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			}))
			diags.requireNoError(t)
			if diags.hasWarning("No template or config set") {
				t.Errorf("an unknown %s produced a warning about a missing source; got:\n%s", attr, diags.text())
			}
		})
	}
}

// Terraform calls ValidateResourceConfig before `dynamic` blocks are expanded, so
// the list attributes arrive unknown. Decoding them into Go slices failed with a
// Value Conversion Error before planning began; validation now tolerates unknown
// lists and defers the checks that depend on their contents.
func TestInstanceValidateConfigToleratesUnknownLists(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	obj := h.objectType()
	for _, attr := range []string{attrMounts, attrPortForwards, attrProvisions} {
		t.Run(attr+" is unknown", func(t *testing.T) {
			t.Parallel()

			diags := h.validateConfig(h.value(map[string]tftypes.Value{
				"name":     tfString(testInstanceName),
				"template": tfString("template:alpine"),
				attr:       tftypes.NewValue(obj.AttributeTypes[attr], tftypes.UnknownValue),
			}))
			diags.requireNoError(t)
		})
	}
}

// The instance name still has to be validated even when the lists are unknown:
// it does not depend on them, and deferring everything would lose a plan-time
// check for no reason.
func TestInstanceValidateConfigStillChecksTheNameWhenListsAreUnknown(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	obj := h.objectType()
	diags := h.validateConfig(h.value(map[string]tftypes.Value{
		"name":     tfString("Not A Valid Lima Name!"),
		"template": tfString("template:alpine"),
		attrMounts: tftypes.NewValue(obj.AttributeTypes[attrMounts], tftypes.UnknownValue),
	}))
	if !diags.HasError() {
		t.Errorf("an invalid instance name passed validation because the lists were unknown; got:\n%s", diags.text())
	}
}

// Lima's socket paths must fit in UNIX_PATH_MAX=104, and the check needs the
// resolved home. It used to return early when no `home` was configured — the
// default installation — so the failure it exists to pre-empt still arrived from
// Lima mid-apply.
func TestInstanceValidateConfigEnforcesTheSocketPathLimit(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	longHome := "/Users/somebody/very/deeply/nested/directory/for/lima/instances/home"
	h := newResourceHarness(t, fake, instanceResourceCtor, func(d *providerData) {
		d.Home = longHome
	})

	diags := h.validateConfig(planForCreate(h, strings.Repeat("n", 60), nil))
	if !diags.HasError() {
		t.Errorf("a name that cannot fit in a unix socket path passed plan-time validation; got:\n%s", diags.text())
	}
}

// Plain mode makes Lima ignore mounts and port forwards outright and skip the
// guest agent that implements forwarding. Nothing fails — the settings simply do
// nothing — so the only symptom is a service that cannot be reached, which is
// exactly the case worth warning about.
func TestInstanceValidateConfigWarnsAboutPlainModeWithForwards(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	h := newResourceHarness(t, fake, instanceResourceCtor)

	obj := h.objectType()
	portForwards, ok := obj.AttributeTypes[attrPortForwards].(tftypes.List)
	if !ok {
		t.Fatalf("%s is %T, want a list", attrPortForwards, obj.AttributeTypes[attrPortForwards])
	}
	entry, ok := portForwards.ElementType.(tftypes.Object)
	if !ok {
		t.Fatalf("%s element is %T, want an object", attrPortForwards, portForwards.ElementType)
	}
	entryValues := make(map[string]tftypes.Value, len(entry.AttributeTypes))
	for name, attrType := range entry.AttributeTypes {
		entryValues[name] = tftypes.NewValue(attrType, nil)
	}
	entryValues["guest_port"] = tfNumber(8080)

	diags := h.validateConfig(h.value(map[string]tftypes.Value{
		"name":             tfString(testInstanceName),
		"template":         tfString("template:alpine"),
		"config_overrides": tfString("plain: true\n"),
		attrPortForwards: tftypes.NewValue(portForwards, []tftypes.Value{
			tftypes.NewValue(entry, entryValues),
		}),
	}))
	diags.requireNoError(t)

	if !diags.hasWarning("plain") {
		t.Errorf("plain mode alongside port forwards produced no warning; got:\n%s", diags.text())
	}
}

// Declaring a template for an imported instance is a claim the provider cannot
// confirm but can sometimes refute, by comparing resolved disk images. A refuted
// claim warns rather than fails: the value only matters if the instance is later
// replaced.
func TestUpdateWarnsWhenAnAdoptedTemplateCannotBeTheRightOne(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	// Seeded with Fedora's image, then told it came from alpine.
	fake.Seed(testutil.FakeInstance{
		Name:   testInstanceName,
		Status: "Stopped",
		Config: testutil.FakeConfig{
			Images: []testutil.FakeImage{{
				Location: "https://download.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/Fedora-Cloud-Base-44.x86_64.qcow2",
			}},
		},
	})
	h := newResourceHarness(t, fake, instanceResourceCtor)

	// state has no template (as after an import); the plan declares one.
	state := h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
		"start":         tfBool(false),
		"protect":       tfBool(false),
	})
	plan := h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
		"template":      tfString("template:alpine"),
		"start":         tfBool(false),
		"protect":       tfBool(false),
	})

	_, diags := h.update(state, plan)
	diags.requireNoError(t)

	if !diags.hasWarning("does not match") {
		t.Errorf("adopting a template the images refute produced no warning; diagnostics:\n%s", diags.text())
	}
}

// The mirror image: a template consistent with the instance's images must not
// warn, or the warning becomes noise that people learn to ignore.
func TestUpdateDoesNotWarnForAConsistentAdoptedTemplate(t *testing.T) {
	t.Parallel()

	fake := testutil.NewFakeLimactl()
	fake.Seed(testutil.FakeInstance{
		Name:   testInstanceName,
		Status: "Stopped",
		Config: testutil.FakeConfig{
			Images: []testutil.FakeImage{{
				Location: "https://dl-cdn.alpinelinux.org/alpine/v3.23/releases/cloud/nocloud_alpine-3.23.4-x86_64-uefi-cloudinit-r0.qcow2",
			}},
		},
	})
	h := newResourceHarness(t, fake, instanceResourceCtor)

	state := h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
		"start":         tfBool(false),
		"protect":       tfBool(false),
	})
	plan := h.value(map[string]tftypes.Value{
		"name":          tfString(testInstanceName),
		"instance_name": tfString(testInstanceName),
		"template":      tfString("template:alpine"),
		"start":         tfBool(false),
		"protect":       tfBool(false),
	})

	_, diags := h.update(state, plan)
	diags.requireNoError(t)

	if diags.hasWarning("does not match") {
		t.Errorf("a consistent template produced a mismatch warning; diagnostics:\n%s", diags.text())
	}
}
