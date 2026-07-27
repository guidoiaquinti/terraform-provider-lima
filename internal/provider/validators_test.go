package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// runStringValidator exercises a validator and reports whether it produced
// errors and warnings.
func runStringValidator(v validator.String, value types.String) (errs, warns int) {
	resp := &validator.StringResponse{}
	v.ValidateString(context.Background(), validator.StringRequest{
		Path:        path.Root("test"),
		ConfigValue: value,
	}, resp)
	return resp.Diagnostics.ErrorsCount(), resp.Diagnostics.WarningsCount()
}

func TestInstanceNameValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{name: "valid", value: types.StringValue("project-dev")},
		{name: "null is skipped", value: types.StringNull()},
		{name: "unknown is skipped", value: types.StringUnknown()},
		{name: "empty", value: types.StringValue(""), wantErr: true},
		{name: "space", value: types.StringValue("my vm"), wantErr: true},
		{name: "shell metacharacters", value: types.StringValue("dev;rm -rf /"), wantErr: true},
		{name: "leading dash looks like a flag", value: types.StringValue("--help"), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs, _ := runStringValidator(InstanceName(), tc.value)
			if tc.wantErr != (errs > 0) {
				t.Errorf("errors = %d, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

func TestSizeValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{name: "GiB", value: types.StringValue("4GiB")},
		{name: "MiB", value: types.StringValue("8192MiB")},
		{name: "null is skipped", value: types.StringNull()},
		{name: "nonsense", value: types.StringValue("lots"), wantErr: true},
		{name: "unknown unit", value: types.StringValue("4XB"), wantErr: true},
		{name: "zero", value: types.StringValue("0GiB"), wantErr: true},
		{name: "negative", value: types.StringValue("-1GiB"), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs, _ := runStringValidator(Size("memory"), tc.value)
			if tc.wantErr != (errs > 0) {
				t.Errorf("errors = %d, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

func TestYAMLValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{name: "valid mapping", value: types.StringValue("cpus: 4\n")},
		{name: "nested mapping", value: types.StringValue("ssh:\n  localPort: 22\n")},
		{name: "null is skipped", value: types.StringNull()},
		{name: "empty string is allowed", value: types.StringValue("")},
		{name: "malformed", value: types.StringValue("a: [1,2\n"), wantErr: true},
		{name: "sequence is not a mapping", value: types.StringValue("- a\n"), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs, _ := runStringValidator(YAML("config"), tc.value)
			if tc.wantErr != (errs > 0) {
				t.Errorf("errors = %d, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

func TestYAMLValidatorDoesNotEchoContent(t *testing.T) {
	t.Parallel()

	// The document may hold secrets, so the diagnostic must not quote it.
	secret := "password: hunter2\n  bad: indent\n"
	resp := &validator.StringResponse{}
	YAML("config").ValidateString(context.Background(), validator.StringRequest{
		Path:        path.Root("config"),
		ConfigValue: types.StringValue(secret),
	}, resp)

	for _, d := range resp.Diagnostics {
		if contains(d.Detail(), "hunter2") || contains(d.Summary(), "hunter2") {
			t.Errorf("diagnostic leaked configuration content: %s / %s", d.Summary(), d.Detail())
		}
	}
}

func TestKnownValueValidatorWarnsRatherThanFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     types.String
		wantErrs  int
		wantWarns int
	}{
		{name: "known value is clean", value: types.StringValue("vz")},
		{name: "another known value", value: types.StringValue("qemu")},
		{name: "null is skipped", value: types.StringNull()},
		{
			// Forward compatibility: an unrecognised backend must not block
			// the plan, because a newer Lima may well support it.
			name: "unknown value warns", value: types.StringValue("future-hypervisor"), wantWarns: 1,
		},
		{name: "empty string is an error", value: types.StringValue(""), wantErrs: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs, warns := runStringValidator(KnownValue("vm_type", []string{"vz", "qemu"}), tc.value)
			if errs != tc.wantErrs {
				t.Errorf("errors = %d, want %d", errs, tc.wantErrs)
			}
			if warns != tc.wantWarns {
				t.Errorf("warnings = %d, want %d", warns, tc.wantWarns)
			}
		})
	}
}

func TestOneOfValidatorFails(t *testing.T) {
	t.Parallel()

	// protocol is a genuinely closed set, so an unknown value is an error.
	if errs, _ := runStringValidator(OneOf("protocol", []string{"tcp", "udp"}), types.StringValue("sctp")); errs == 0 {
		t.Error("OneOf accepted a value outside the allowed set")
	}
	if errs, _ := runStringValidator(OneOf("protocol", []string{"tcp", "udp"}), types.StringValue("tcp")); errs != 0 {
		t.Error("OneOf rejected an allowed value")
	}
}

func TestAbsolutePathValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{name: "absolute", value: types.StringValue("/Users/alice/project")},
		{name: "tilde is accepted and expanded later", value: types.StringValue("~/project")},
		{name: "null is skipped", value: types.StringNull()},
		{name: "relative", value: types.StringValue("project"), wantErr: true},
		{name: "dot relative", value: types.StringValue("./project"), wantErr: true},
		{name: "empty", value: types.StringValue(""), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs, _ := runStringValidator(AbsolutePath(), tc.value)
			if tc.wantErr != (errs > 0) {
				t.Errorf("errors = %d, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

func TestNonEmptyValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{name: "content", value: types.StringValue("#!/bin/sh\necho hi\n")},
		{name: "null is skipped", value: types.StringNull()},
		{name: "empty", value: types.StringValue(""), wantErr: true},
		{name: "whitespace only", value: types.StringValue("  \n\t "), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs, _ := runStringValidator(NonEmpty("script"), tc.value)
			if tc.wantErr != (errs > 0) {
				t.Errorf("errors = %d, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

func TestDurationValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   types.String
		wantErr bool
	}{
		{name: "minutes", value: types.StringValue("20m")},
		{name: "compound", value: types.StringValue("1h30m")},
		{name: "null is skipped", value: types.StringNull()},
		{name: "nonsense", value: types.StringValue("soon"), wantErr: true},
		{name: "no unit", value: types.StringValue("20"), wantErr: true},
		{name: "zero", value: types.StringValue("0s"), wantErr: true},
		{name: "negative", value: types.StringValue("-5m"), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			errs, _ := runStringValidator(Duration(), tc.value)
			if tc.wantErr != (errs > 0) {
				t.Errorf("errors = %d, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

// runStringPlanModifier exercises a plan modifier over a state/config pair.
func runStringPlanModifier(m planmodifier.String, state, config types.String) (types.String, int) {
	resp := &planmodifier.StringResponse{PlanValue: config}
	m.PlanModifyString(context.Background(), planmodifier.StringRequest{
		Path:        path.Root("test"),
		StateValue:  state,
		ConfigValue: config,
		PlanValue:   config,
	}, resp)
	return resp.PlanValue, resp.Diagnostics.ErrorsCount()
}

func TestDiskGrowOnlyModifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		state   types.String
		config  types.String
		wantErr bool
	}{
		{name: "growth is allowed", state: types.StringValue("50GiB"), config: types.StringValue("100GiB")},
		{name: "same size", state: types.StringValue("50GiB"), config: types.StringValue("50GiB")},
		{
			// Equivalent spellings must not read as a shrink.
			name: "equivalent spelling", state: types.StringValue("50GiB"), config: types.StringValue("51200MiB"),
		},
		{
			// Shrinking is rejected because Lima cannot shrink and a
			// replacement would destroy the disk's data.
			name: "shrink is rejected", state: types.StringValue("100GiB"), config: types.StringValue("50GiB"), wantErr: true,
		},
		{name: "no prior state means create", state: types.StringNull(), config: types.StringValue("10GiB")},
		{name: "null config is skipped", state: types.StringValue("100GiB"), config: types.StringNull()},
		{name: "unparseable state is skipped", state: types.StringValue("???"), config: types.StringValue("1GiB")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, errs := runStringPlanModifier(DiskGrowOnly(), tc.state, tc.config)
			if tc.wantErr != (errs > 0) {
				t.Errorf("errors = %d, wantErr %v", errs, tc.wantErr)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// runReplaceModifier exercises ReplaceOnRealChange over a state/config pair.
// rawNull models create (null state) and destroy (null plan), which the
// modifier must never act on.
func runReplaceModifier(t *testing.T, state, config types.String) bool {
	t.Helper()

	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"v": tftypes.String}}
	nonNull := tftypes.NewValue(obj, map[string]tftypes.Value{"v": tftypes.NewValue(tftypes.String, "x")})

	resp := &planmodifier.StringResponse{PlanValue: config}
	ReplaceOnRealChange().PlanModifyString(context.Background(), planmodifier.StringRequest{
		Path:        path.Root("v"),
		StateValue:  state,
		ConfigValue: config,
		PlanValue:   config,
		State:       tfsdk.State{Raw: nonNull},
		Plan:        tfsdk.Plan{Raw: nonNull},
	}, resp)
	return resp.RequiresReplace
}

func TestReplaceOnRealChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		state       types.String
		config      types.String
		wantReplace bool
	}{
		{
			name:        "a real change between two known values replaces",
			state:       types.StringValue("vz"),
			config:      types.StringValue("qemu"),
			wantReplace: true,
		},
		{
			name:   "an unchanged value does not replace",
			state:  types.StringValue("vz"),
			config: types.StringValue("vz"),
		},
		{
			// Adoption: an imported instance has no recorded template, so the
			// user declaring one describes what already exists. Recreating
			// the VM there would destroy data to end up where it started.
			name:   "adopting an imported instance does not replace",
			state:  types.StringNull(),
			config: types.StringValue("template:ubuntu"),
		},
		{
			// Removing an attribute means "stop managing this", not
			// "rebuild the machine".
			name:   "un-managing an attribute does not replace",
			state:  types.StringValue("template:ubuntu"),
			config: types.StringNull(),
		},
		{
			name:   "an unknown configured value defers",
			state:  types.StringValue("vz"),
			config: types.StringUnknown(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := runReplaceModifier(t, tc.state, tc.config); got != tc.wantReplace {
				t.Errorf("RequiresReplace = %v, want %v", got, tc.wantReplace)
			}
		})
	}
}

func TestReplaceOnRealChangeIgnoresCreateAndDestroy(t *testing.T) {
	t.Parallel()

	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"v": tftypes.String}}
	nullRaw := tftypes.NewValue(obj, nil)
	nonNull := tftypes.NewValue(obj, map[string]tftypes.Value{"v": tftypes.NewValue(tftypes.String, "x")})

	cases := []struct {
		name        string
		state, plan tftypes.Value
	}{
		{name: "create has a null prior state", state: nullRaw, plan: nonNull},
		{name: "destroy has a null plan", state: nonNull, plan: nullRaw},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := &planmodifier.StringResponse{}
			ReplaceOnRealChange().PlanModifyString(context.Background(), planmodifier.StringRequest{
				Path:        path.Root("v"),
				StateValue:  types.StringValue("a"),
				ConfigValue: types.StringValue("b"),
				State:       tfsdk.State{Raw: tc.state},
				Plan:        tfsdk.Plan{Raw: tc.plan},
			}, resp)
			if resp.RequiresReplace {
				t.Error("the modifier must not force replacement outside an update")
			}
		})
	}
}
