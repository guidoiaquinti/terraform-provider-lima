package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// These tests cover the plan-time validation and state-mapping logic that runs
// without any Lima interaction. The full CRUD paths are covered by the
// acceptance tests in acceptance_test.go, which drive real VMs.

func TestValidateInstanceConfigInstanceSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     instanceModel
		wantErrs  int
		wantWarns int
	}{
		{
			name: "template alone is fine",
			model: instanceModel{
				Name:     types.StringValue("dev"),
				Template: types.StringValue("template:ubuntu"),
			},
		},
		{
			name: "config alone is fine",
			model: instanceModel{
				Name:   types.StringValue("dev"),
				Config: types.StringValue("cpus: 2\n"),
			},
		},
		{
			// Nothing at all would fail confusingly inside Lima, so it is
			// rejected up front.
			name:     "neither source and no typed attributes is an error",
			model:    instanceModel{Name: types.StringValue("dev")},
			wantErrs: 1,
		},
		{
			// Typed attributes alone are workable, but relying entirely on
			// Lima's default image is worth flagging.
			name: "typed attributes without a source warns",
			model: instanceModel{
				Name: types.StringValue("dev"),
				CPUs: types.Int64Value(2),
			},
			wantWarns: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := validateInstanceConfig(&tc.model, declaredLists{}, "", "")
			if diags.ErrorsCount() != tc.wantErrs {
				t.Errorf("errors = %d, want %d: %v", diags.ErrorsCount(), tc.wantErrs, diags)
			}
			if diags.WarningsCount() != tc.wantWarns {
				t.Errorf("warnings = %d, want %d: %v", diags.WarningsCount(), tc.wantWarns, diags)
			}
		})
	}
}

func TestValidateInstanceConfigDuplicatePortForwards(t *testing.T) {
	t.Parallel()

	pf := func(guest, host int64, proto string) portModel {
		m := portModel{GuestPort: types.Int64Value(guest), Protocol: types.StringValue(proto)}
		if host > 0 {
			m.HostPort = types.Int64Value(host)
		} else {
			m.HostPort = types.Int64Null()
		}
		return m
	}

	tests := []struct {
		name     string
		forwards []portModel
		wantErrs int
	}{
		{
			name:     "distinct forwards",
			forwards: []portModel{pf(80, 8080, "tcp"), pf(443, 8443, "tcp")},
		},
		{
			// The same guest port under a different protocol is a legitimate
			// configuration, not a duplicate.
			name:     "same port different protocol",
			forwards: []portModel{pf(53, 5353, "tcp"), pf(53, 5354, "udp")},
		},
		{
			name:     "duplicate guest port",
			forwards: []portModel{pf(80, 8080, "tcp"), pf(80, 8081, "tcp")},
			wantErrs: 1,
		},
		{
			// Two forwards cannot bind the same host port.
			name:     "duplicate host port",
			forwards: []portModel{pf(80, 8080, "tcp"), pf(443, 8080, "tcp")},
			wantErrs: 1,
		},
		{
			name:     "duplicate guest and host port together",
			forwards: []portModel{pf(80, 8080, "tcp"), pf(80, 8080, "tcp")},
			wantErrs: 2,
		},
		{
			// Omitted host ports let Lima choose, so they cannot collide.
			name:     "omitted host ports do not collide",
			forwards: []portModel{pf(80, 0, "tcp"), pf(443, 0, "tcp")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := instanceModel{
				Name:     types.StringValue("dev"),
				Template: types.StringValue("template:ubuntu"),
			}
			lists := declaredLists{PortForwards: tc.forwards, PortForwardsManaged: true}
			diags := validateInstanceConfig(&model, lists, "", "")
			if diags.ErrorsCount() != tc.wantErrs {
				t.Errorf("errors = %d, want %d: %v", diags.ErrorsCount(), tc.wantErrs, diags)
			}
		})
	}
}

func TestValidateInstanceConfigDuplicateMounts(t *testing.T) {
	t.Parallel()

	mount := func(loc string) mountModel {
		return mountModel{
			Location:   types.StringValue(loc),
			MountPoint: types.StringNull(),
			Writable:   types.BoolValue(false),
		}
	}

	tests := []struct {
		name     string
		mounts   []mountModel
		wantErrs int
	}{
		{
			name:   "distinct locations",
			mounts: []mountModel{mount("/a"), mount("/b")},
		},
		{
			name:     "identical locations",
			mounts:   []mountModel{mount("/a"), mount("/a")},
			wantErrs: 1,
		},
		{
			// Duplicates must be caught after normalisation, not before, or
			// "/a/" and "/a" would slip through.
			name:     "locations that normalise to the same path",
			mounts:   []mountModel{mount("/a"), mount("/a/"), mount("/x/../a")},
			wantErrs: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := instanceModel{
				Name:     types.StringValue("dev"),
				Template: types.StringValue("template:ubuntu"),
			}
			lists := declaredLists{Mounts: tc.mounts, MountsManaged: true}
			diags := validateInstanceConfig(&model, lists, "", "")
			if diags.ErrorsCount() != tc.wantErrs {
				t.Errorf("errors = %d, want %d: %v", diags.ErrorsCount(), tc.wantErrs, diags)
			}
		})
	}
}

func TestValidateInstanceConfigNameWithPrefixAndHome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		instance string
		prefix   string
		home     string
		wantErrs int
		wantText string
	}{
		{
			name:     "plain name",
			instance: "dev",
		},
		{
			name:     "prefixed name is still valid",
			instance: "dev",
			prefix:   "acme-",
		},
		{
			// The prefix is part of the real Lima name, so it must not make
			// the result invalid.
			name:     "prefix makes the name invalid",
			instance: "dev",
			prefix:   "-bad-",
			wantErrs: 1,
			wantText: "name_prefix",
		},
		{
			// The exact failure mode observed against real Lima: a long home
			// plus a name overflows UNIX_PATH_MAX.
			name:     "name too long for a deep LIMA_HOME",
			instance: "development-environment",
			home:     "/Users/alice/Library/Application Support/some/deeply/nested/lima/home/directory",
			wantErrs: 1,
			wantText: "too long",
		},
		{
			name:     "short home is fine",
			instance: "development-environment",
			home:     "/tmp/lima",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := instanceModel{
				Name:     types.StringValue(tc.instance),
				Template: types.StringValue("template:ubuntu"),
			}
			diags := validateInstanceConfig(&model, declaredLists{}, tc.prefix, tc.home)
			if diags.ErrorsCount() != tc.wantErrs {
				t.Fatalf("errors = %d, want %d: %v", diags.ErrorsCount(), tc.wantErrs, diags)
			}
			if tc.wantText != "" {
				found := false
				for _, d := range diags {
					if strings.Contains(d.Detail(), tc.wantText) || strings.Contains(d.Summary(), tc.wantText) {
						found = true
					}
				}
				if !found {
					t.Errorf("no diagnostic mentioned %q: %v", tc.wantText, diags)
				}
			}
		})
	}
}

func TestValidateInstanceConfigSkipsUnknownValues(t *testing.T) {
	t.Parallel()

	// Values that are not known until apply must not trip validation, or a
	// configuration deriving a name from another resource could never plan.
	model := instanceModel{
		Name:     types.StringUnknown(),
		Template: types.StringUnknown(),
	}
	lists := declaredLists{
		PortForwards: []portModel{{
			GuestPort: types.Int64Unknown(),
			HostPort:  types.Int64Unknown(),
			Protocol:  types.StringValue("tcp"),
		}},
		PortForwardsManaged: true,
		Mounts: []mountModel{{
			Location:   types.StringUnknown(),
			MountPoint: types.StringNull(),
			Writable:   types.BoolValue(false),
		}},
		MountsManaged: true,
	}
	diags := validateInstanceConfig(&model, lists, "acme-", "/tmp/lima")
	if diags.ErrorsCount() != 0 {
		t.Errorf("unknown values produced errors: %v", diags)
	}
}

func TestToRenderRequestExpandsMountPaths(t *testing.T) {
	t.Parallel()

	model := instanceModel{
		Template: types.StringValue("template:ubuntu"),
		CPUs:     types.Int64Value(4),
		Memory:   types.StringValue("8GiB"),
	}
	lists := declaredLists{
		Mounts: []mountModel{{
			// A path needing normalisation must reach Lima cleaned.
			Location:   types.StringValue("/tmp/x/../project/"),
			MountPoint: types.StringValue("/workspace"),
			Writable:   types.BoolValue(true),
		}},
		MountsManaged: true,
		PortForwards: []portModel{{
			GuestPort: types.Int64Value(80),
			HostPort:  types.Int64Value(8080),
			Protocol:  types.StringValue("tcp"),
			GuestIP:   types.StringNull(),
			HostIP:    types.StringNull(),
		}},
		PortForwardsManaged: true,
		Provisions: []provModel{{
			Mode:       types.StringValue("system"),
			Script:     types.StringValue("#!/bin/sh\necho hi\n"),
			RerunToken: types.StringValue("abc123"),
		}},
	}

	req, diags := model.toRenderRequest(lists)
	if diags.HasError() {
		t.Fatalf("toRenderRequest: %v", diags)
	}

	if req.Template != "template:ubuntu" {
		t.Errorf("Template = %q", req.Template)
	}
	if req.Typed.CPUs != 4 || req.Typed.Memory != "8GiB" {
		t.Errorf("typed attributes = %+v", req.Typed)
	}
	if len(req.Typed.Mounts) != 1 || req.Typed.Mounts[0].Location != "/tmp/project" {
		t.Errorf("mount location was not normalised: %+v", req.Typed.Mounts)
	}
	if !req.Typed.Mounts[0].Writable {
		t.Error("mount writable flag was lost")
	}
	if len(req.Typed.PortForwards) != 1 || req.Typed.PortForwards[0].HostPort != 8080 {
		t.Errorf("port forward = %+v", req.Typed.PortForwards)
	}
	// rerun_token exists only to force replacement; it must never reach the
	// generated Lima document, which has no such field.
	if len(req.Typed.Provision) != 1 || req.Typed.Provision[0].Mode != "system" {
		t.Errorf("provision = %+v", req.Typed.Provision)
	}
}

func TestRerunTokenDoesNotReachTheDocument(t *testing.T) {
	t.Parallel()

	model := instanceModel{Template: types.StringValue("template:ubuntu")}
	lists := declaredLists{
		Provisions: []provModel{{
			Mode:       types.StringValue("system"),
			Script:     types.StringValue("#!/bin/sh\ntrue\n"),
			RerunToken: types.StringValue("sentinel-token-value"),
		}},
	}
	req, diags := model.toRenderRequest(lists)
	if diags.HasError() {
		t.Fatalf("toRenderRequest: %v", diags)
	}
	for _, p := range req.Typed.Provision {
		if strings.Contains(p.Script, "sentinel-token-value") {
			t.Error("rerun_token leaked into the provisioning script")
		}
	}
}

func TestApplyInstanceMapsRuntimeStateOnly(t *testing.T) {
	t.Parallel()

	inst := runningInstance()

	// The user set cpus but not memory. Only the set attribute may be
	// updated from Lima; otherwise Lima's resolved default would appear as a
	// permanent diff.
	model := instanceModel{
		Name:     types.StringValue("dev"),
		Template: types.StringValue("template:ubuntu"),
		CPUs:     types.Int64Value(2),
		Memory:   types.StringNull(),
	}
	model.applyInstance(inst)
	model.applyObservedConfig(inst)

	if model.Status.ValueString() != "running" {
		t.Errorf("status = %q, want running", model.Status.ValueString())
	}
	if model.RawStatus.ValueString() != "Running" {
		t.Errorf("raw_status = %q, want Running", model.RawStatus.ValueString())
	}
	if model.SSHPort.ValueInt64() != 61627 {
		t.Errorf("ssh_port = %d", model.SSHPort.ValueInt64())
	}
	if model.SSHUser.ValueString() != "alice" {
		t.Errorf("ssh_user = %q", model.SSHUser.ValueString())
	}

	// Drift in an attribute the user pinned must surface.
	if model.CPUs.ValueInt64() != 8 {
		t.Errorf("cpus = %d, want the observed 8", model.CPUs.ValueInt64())
	}
	// An unset attribute must stay unset.
	if !model.Memory.IsNull() {
		t.Errorf("memory = %v, want it to stay null so Lima's default does not become a diff", model.Memory)
	}
	// Desired configuration must never be overwritten.
	if model.Template.ValueString() != "template:ubuntu" {
		t.Errorf("template was overwritten: %q", model.Template.ValueString())
	}
}

func TestApplyInstanceNullsEmptyValues(t *testing.T) {
	t.Parallel()

	// A stopped instance that never ran reports no port. That must read as
	// absent rather than as zero.
	var model instanceModel
	model.applyInstance(limaInstanceWithoutSSH())

	if !model.SSHPort.IsNull() {
		t.Errorf("ssh_port = %v, want null", model.SSHPort)
	}
	if !model.SSHAddress.IsNull() {
		t.Errorf("ssh_address = %v, want null", model.SSHAddress)
	}
	if model.Status.ValueString() != "stopped" {
		t.Errorf("status = %q", model.Status.ValueString())
	}
}

func TestInstanceDataSourceModelMapping(t *testing.T) {
	t.Parallel()

	var model instanceDataSourceModel
	model.applyInstance(runningInstance(), "2.2.0")

	checks := map[string]struct{ got, want any }{
		"id":        {model.ID.ValueString(), "tfdisco"},
		"status":    {model.Status.ValueString(), "running"},
		"cpus":      {model.CPUs.ValueInt64(), int64(8)},
		"memory":    {model.Memory.ValueString(), "1GiB"},
		"disk":      {model.Disk.ValueString(), "8GiB"},
		"protected": {model.Protected.ValueBool(), false},
		"version":   {model.LimaVersion.ValueString(), "2.2.0"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", name, c.got, c.want)
		}
	}
}

func TestInstanceDataSourceModelFallsBackToProviderVersion(t *testing.T) {
	t.Parallel()

	inst := runningInstance()
	inst.LimaVersion = ""

	var model instanceDataSourceModel
	model.applyInstance(inst, "2.3.0")

	if model.LimaVersion.ValueString() != "2.3.0" {
		t.Errorf("lima_version = %q, want the provider-detected 2.3.0", model.LimaVersion.ValueString())
	}
}

// runningInstance returns a realistic running instance, matching the shape of
// the captured Lima 2.2.0 fixtures.
func runningInstance() lima.Instance {
	inst := lima.Instance{
		Name:          "tfdisco",
		Hostname:      "lima-tfdisco",
		RawStatus:     "Running",
		Dir:           "/private/tmp/ltfdisco/tfdisco",
		VMType:        "vz",
		Arch:          "aarch64",
		CPUs:          8,
		MemoryBytes:   1 << 30,
		DiskBytes:     8 << 30,
		SSHLocalPort:  61627,
		SSHConfigFile: "/private/tmp/ltfdisco/tfdisco/ssh.config",
		SSHAddress:    "127.0.0.1",
		LimaVersion:   "2.2.0",
	}
	inst.Config.User.Name = "alice"
	return inst
}

// limaInstanceWithoutSSH returns a stopped instance that has never run, so
// Lima reports no SSH endpoint.
func limaInstanceWithoutSSH() lima.Instance {
	return lima.Instance{
		Name:      "fresh",
		Hostname:  "lima-fresh",
		RawStatus: "Stopped",
	}
}

func TestResizeRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		plan  instanceModel
		state instanceModel
		want  lima.EditRequest
	}{
		{
			name:  "no change produces an empty request",
			plan:  instanceModel{CPUs: types.Int64Value(4), Memory: types.StringValue("8GiB")},
			state: instanceModel{CPUs: types.Int64Value(4), Memory: types.StringValue("8GiB")},
			want:  lima.EditRequest{},
		},
		{
			name:  "cpus increase",
			plan:  instanceModel{CPUs: types.Int64Value(8), Memory: types.StringNull(), Disk: types.StringNull()},
			state: instanceModel{CPUs: types.Int64Value(4), Memory: types.StringNull(), Disk: types.StringNull()},
			want:  lima.EditRequest{CPUs: 8},
		},
		{
			name:  "cpus decrease is still an in-place change",
			plan:  instanceModel{CPUs: types.Int64Value(2), Memory: types.StringNull(), Disk: types.StringNull()},
			state: instanceModel{CPUs: types.Int64Value(4), Memory: types.StringNull(), Disk: types.StringNull()},
			want:  lima.EditRequest{CPUs: 2},
		},
		{
			// Sizes are compared as bytes, so an equivalent spelling is not a
			// change and must not trigger a needless stop/start cycle.
			name:  "equivalent memory spelling is not a change",
			plan:  instanceModel{CPUs: types.Int64Null(), Memory: types.StringValue("8192MiB"), Disk: types.StringNull()},
			state: instanceModel{CPUs: types.Int64Null(), Memory: types.StringValue("8GiB"), Disk: types.StringNull()},
			want:  lima.EditRequest{},
		},
		{
			name:  "memory change",
			plan:  instanceModel{CPUs: types.Int64Null(), Memory: types.StringValue("16GiB"), Disk: types.StringNull()},
			state: instanceModel{CPUs: types.Int64Null(), Memory: types.StringValue("8GiB"), Disk: types.StringNull()},
			want:  lima.EditRequest{MemoryBytes: 16 << 30},
		},
		{
			name:  "disk growth",
			plan:  instanceModel{CPUs: types.Int64Null(), Memory: types.StringNull(), Disk: types.StringValue("100GiB")},
			state: instanceModel{CPUs: types.Int64Null(), Memory: types.StringNull(), Disk: types.StringValue("50GiB")},
			want:  lima.EditRequest{DiskBytes: 100 << 30},
		},
		{
			name:  "all three at once",
			plan:  instanceModel{CPUs: types.Int64Value(8), Memory: types.StringValue("16GiB"), Disk: types.StringValue("100GiB")},
			state: instanceModel{CPUs: types.Int64Value(4), Memory: types.StringValue("8GiB"), Disk: types.StringValue("50GiB")},
			want:  lima.EditRequest{CPUs: 8, MemoryBytes: 16 << 30, DiskBytes: 100 << 30},
		},
		{
			// Newly setting an attribute that was previously unmanaged is a
			// change worth applying.
			name:  "attribute newly set in configuration",
			plan:  instanceModel{CPUs: types.Int64Value(4), Memory: types.StringNull(), Disk: types.StringNull()},
			state: instanceModel{CPUs: types.Int64Null(), Memory: types.StringNull(), Disk: types.StringNull()},
			want:  lima.EditRequest{CPUs: 4},
		},
		{
			// Removing an attribute from configuration must NOT resize: Lima
			// has no "revert to default" for an existing instance, and
			// guessing could shrink the VM.
			name:  "attribute removed from configuration is left alone",
			plan:  instanceModel{CPUs: types.Int64Null(), Memory: types.StringNull(), Disk: types.StringNull()},
			state: instanceModel{CPUs: types.Int64Value(8), Memory: types.StringValue("16GiB"), Disk: types.StringValue("100GiB")},
			want:  lima.EditRequest{},
		},
		{
			name:  "unknown values are skipped",
			plan:  instanceModel{CPUs: types.Int64Unknown(), Memory: types.StringUnknown(), Disk: types.StringUnknown()},
			state: instanceModel{CPUs: types.Int64Value(4), Memory: types.StringValue("8GiB"), Disk: types.StringValue("50GiB")},
			want:  lima.EditRequest{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, diags := resizeRequest(&tc.plan, &tc.state)
			if diags.HasError() {
				t.Fatalf("resizeRequest: %v", diags)
			}
			if got != tc.want {
				t.Errorf("resizeRequest = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestResizeRequestRejectsInvalidSize(t *testing.T) {
	t.Parallel()

	plan := instanceModel{
		CPUs:   types.Int64Null(),
		Memory: types.StringValue("not-a-size"),
		Disk:   types.StringNull(),
	}
	state := instanceModel{CPUs: types.Int64Null(), Memory: types.StringValue("8GiB"), Disk: types.StringNull()}

	_, diags := resizeRequest(&plan, &state)
	if !diags.HasError() {
		t.Error("an unparseable size should produce a diagnostic")
	}
}

func TestObservedSizePreservesEquivalentSpelling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		configured    types.String
		observedBytes int64
		want          types.String
	}{
		{
			// The property that prevents a perpetual diff: an equivalent
			// spelling is left exactly as the user wrote it. Terraform core
			// compares raw config to prior state before plan modifiers run,
			// so rewriting this to "2GiB" made every plan show a change whose
			// apply did nothing.
			name:          "equivalent spelling is preserved",
			configured:    types.StringValue("2048MiB"),
			observedBytes: 2 << 30,
			want:          types.StringValue("2048MiB"),
		},
		{
			name:          "identical spelling is preserved",
			configured:    types.StringValue("2GiB"),
			observedBytes: 2 << 30,
			want:          types.StringValue("2GiB"),
		},
		{
			// Real drift must still surface, in canonical form.
			name:          "a genuine difference is overwritten",
			configured:    types.StringValue("2GiB"),
			observedBytes: 4 << 30,
			want:          types.StringValue("4GiB"),
		},
		{
			name:          "unset stays unset so Lima's default is not adopted",
			configured:    types.StringNull(),
			observedBytes: 4 << 30,
			want:          types.StringNull(),
		},
		{
			name:          "no observation leaves the configured value alone",
			configured:    types.StringValue("2GiB"),
			observedBytes: 0,
			want:          types.StringValue("2GiB"),
		},
		{
			// An unparseable configured value cannot be compared, so the
			// observed truth wins.
			name:          "unparseable configured value is replaced",
			configured:    types.StringValue("wat"),
			observedBytes: 4 << 30,
			want:          types.StringValue("4GiB"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := observedSize(tc.configured, tc.observedBytes)
			if !got.Equal(tc.want) {
				t.Errorf("observedSize(%v, %d) = %v, want %v", tc.configured, tc.observedBytes, got, tc.want)
			}
		})
	}
}

func TestApplyObservedConfigDoesNotChurnSizes(t *testing.T) {
	t.Parallel()

	// A full read cycle must leave a semantically-identical configuration
	// byte-for-byte unchanged, or every plan would be dirty.
	inst := runningInstance()
	inst.MemoryBytes = 2 << 30
	inst.DiskBytes = 8 << 30

	model := instanceModel{
		Memory: types.StringValue("2048MiB"),
		Disk:   types.StringValue("8192MiB"),
		CPUs:   types.Int64Value(inst.CPUs),
	}
	model.applyObservedConfig(inst)

	if model.Memory.ValueString() != "2048MiB" {
		t.Errorf("memory = %q, want the configured spelling to survive a read", model.Memory.ValueString())
	}
	if model.Disk.ValueString() != "8192MiB" {
		t.Errorf("disk = %q, want the configured spelling to survive a read", model.Disk.ValueString())
	}
}

// mountsFixture builds an instance whose resolved mounts mirror what Lima
// produces: the declared entry plus one contributed by the base template.
func mountsFixture(mounts ...lima.MountView) lima.Instance {
	inst := runningInstance()
	inst.Config.Mounts = mounts
	return inst
}

func TestReconcileDeclaredEntriesMounts(t *testing.T) {
	t.Parallel()

	declared := func(location, mountPoint string, writable bool) mountModel {
		m := mountModel{
			Location: types.StringValue(location),
			Writable: types.BoolValue(writable),
		}
		if mountPoint == "" {
			m.MountPoint = types.StringNull()
		} else {
			m.MountPoint = types.StringValue(mountPoint)
		}
		return m
	}

	tests := []struct {
		name     string
		declared []mountModel
		resolved []lima.MountView
		want     []mountModel
	}{
		{
			// The common case: nothing changed, so state must be untouched.
			// Any churn here would be a perpetual diff.
			name:     "matching mount is left alone",
			declared: []mountModel{declared("/private/tmp", "/workspace", true)},
			resolved: []lima.MountView{
				{Location: "/private/tmp", MountPoint: "/workspace", Writable: true},
				{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false},
			},
			want: []mountModel{declared("/private/tmp", "/workspace", true)},
		},
		{
			// An unset mount_point must stay unset while Lima is only
			// reporting its default, which is the location itself.
			name:     "default mount point stays null",
			declared: []mountModel{declared("/private/tmp", "", false)},
			resolved: []lima.MountView{{Location: "/private/tmp", MountPoint: "/private/tmp", Writable: false}},
			want:     []mountModel{declared("/private/tmp", "", false)},
		},
		{
			name:     "changed writability is picked up",
			declared: []mountModel{declared("/private/tmp", "/workspace", true)},
			resolved: []lima.MountView{{Location: "/private/tmp", MountPoint: "/workspace", Writable: false}},
			want:     []mountModel{declared("/private/tmp", "/workspace", false)},
		},
		{
			name:     "changed mount point is picked up",
			declared: []mountModel{declared("/private/tmp", "/workspace", true)},
			resolved: []lima.MountView{{Location: "/private/tmp", MountPoint: "/elsewhere", Writable: true}},
			want:     []mountModel{declared("/private/tmp", "/elsewhere", true)},
		},
		{
			// Dropped from state so the plan proposes adding it back.
			name:     "vanished mount is removed from state",
			declared: []mountModel{declared("/private/tmp", "/workspace", true)},
			resolved: []lima.MountView{{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false}},
			want:     []mountModel{},
		},
		{
			// Template-contributed mounts must never be written into state.
			name:     "template mounts are not adopted",
			declared: []mountModel{},
			resolved: []lima.MountView{{Location: "/Users/alice", MountPoint: "/Users/alice", Writable: false}},
			want:     []mountModel{},
		},
		{
			name:     "macOS tmp indirection is not drift",
			declared: []mountModel{declared("/tmp", "/workspace", true)},
			resolved: []lima.MountView{{Location: "/private/tmp", MountPoint: "/workspace", Writable: true}},
			want:     []mountModel{declared("/tmp", "/workspace", true)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := instanceModel{Mounts: mountsList(t, tc.declared)}
			if diags := reconcileDeclaredEntries(t.Context(), &model, declaredOf(t, &model),
				mountsFixture(tc.resolved...)); diags.HasError() {
				t.Fatalf("reconcileDeclaredEntries: %v", diags)
			}

			// Read back through the same decode the resource uses, so the round
			// trip into the attribute value is part of what is under test.
			got := declaredOf(t, &model).Mounts
			if len(got) != len(tc.want) {
				t.Fatalf("mounts = %+v, want %+v", got, tc.want)
			}
			for i := range tc.want {
				if !got[i].Location.Equal(tc.want[i].Location) ||
					!got[i].MountPoint.Equal(tc.want[i].MountPoint) ||
					!got[i].Writable.Equal(tc.want[i].Writable) {
					t.Errorf("mount %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestReconcileDeclaredEntriesPortForwards(t *testing.T) {
	t.Parallel()

	inst := runningInstance()
	inst.Config.PortForwards = []lima.PortForwardView{
		{GuestPort: 8080, HostPort: 18080, Proto: "tcp", GuestIP: "127.0.0.1", HostIP: "127.0.0.1"},
	}

	t.Run("matching forward is left alone", func(t *testing.T) {
		t.Parallel()
		model := instanceModel{PortForwards: portForwardsList(t, []portModel{{
			GuestPort: types.Int64Value(8080),
			HostPort:  types.Int64Value(18080),
			Protocol:  types.StringValue("tcp"),
		}})}
		if diags := reconcileDeclaredEntries(t.Context(), &model, declaredOf(t, &model), inst); diags.HasError() {
			t.Fatalf("reconcileDeclaredEntries: %v", diags)
		}
		got := declaredOf(t, &model).PortForwards
		if len(got) != 1 || got[0].HostPort.ValueInt64() != 18080 {
			t.Errorf("forwards = %+v, want unchanged", got)
		}
	})

	t.Run("unset host port stays null", func(t *testing.T) {
		t.Parallel()
		model := instanceModel{PortForwards: portForwardsList(t, []portModel{{
			GuestPort: types.Int64Value(8080),
			HostPort:  types.Int64Null(),
			Protocol:  types.StringValue("tcp"),
		}})}
		if diags := reconcileDeclaredEntries(t.Context(), &model, declaredOf(t, &model), inst); diags.HasError() {
			t.Fatalf("reconcileDeclaredEntries: %v", diags)
		}
		got := declaredOf(t, &model).PortForwards
		if !got[0].HostPort.IsNull() {
			t.Errorf("host_port = %v, want it to stay null (Lima picked it)", got[0].HostPort)
		}
	})

	t.Run("vanished forward is removed from state", func(t *testing.T) {
		t.Parallel()
		model := instanceModel{PortForwards: portForwardsList(t, []portModel{{
			GuestPort: types.Int64Value(9999),
			Protocol:  types.StringValue("tcp"),
		}})}
		if diags := reconcileDeclaredEntries(t.Context(), &model, declaredOf(t, &model), inst); diags.HasError() {
			t.Fatalf("reconcileDeclaredEntries: %v", diags)
		}
		if got := declaredOf(t, &model).PortForwards; len(got) != 0 {
			t.Errorf("forwards = %+v, want the missing one dropped", got)
		}
	})
}

func TestReconcileDeclaredEntriesSkipsUnresolvedInstances(t *testing.T) {
	t.Parallel()

	// A broken or half-created instance has no configuration worth comparing,
	// so state must not be rewritten from it.
	for _, status := range []string{"Broken", "Installing", "Hibernating"} {
		inst := mountsFixture()
		inst.RawStatus = status

		model := instanceModel{Mounts: mountsList(t, []mountModel{{
			Location:   types.StringValue("/private/tmp"),
			MountPoint: types.StringNull(),
			Writable:   types.BoolValue(true),
		}})}
		if diags := reconcileDeclaredEntries(t.Context(), &model, declaredOf(t, &model), inst); diags.HasError() {
			t.Fatalf("reconcileDeclaredEntries: %v", diags)
		}

		if got := declaredOf(t, &model).Mounts; len(got) != 1 {
			t.Errorf("status %q: mounts were rewritten to %+v", status, got)
		}
	}
}
