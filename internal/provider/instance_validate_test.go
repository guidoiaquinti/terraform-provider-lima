package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// An unknown config or template says nothing about whether a source was set, so
// the provider must not claim one is missing. This produced a warning on every
// plan for any configuration where `config` referenced another value.
func TestValidateInstanceConfigUnknownSourceIsNotReported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model instanceModel
	}{
		{
			name: "unknown config",
			model: instanceModel{
				Name:   types.StringValue("dev"),
				Config: types.StringUnknown(),
			},
		},
		{
			name: "unknown template",
			model: instanceModel{
				Name:     types.StringValue("dev"),
				Template: types.StringUnknown(),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := validateInstanceConfig(&tc.model, declaredLists{}, "", "")
			if diags.ErrorsCount() != 0 {
				t.Errorf("errors = %d, want 0: %v", diags.ErrorsCount(), diags)
			}
			if diags.WarningsCount() != 0 {
				t.Errorf("warnings = %d, want 0: %v", diags.WarningsCount(), diags)
			}
		})
	}
}

// An unresolved list might yet carry a typed attribute, so "nothing is set"
// cannot be concluded from its emptiness either.
func TestValidateInstanceConfigUnknownListIsNotReported(t *testing.T) {
	t.Parallel()

	model := instanceModel{Name: types.StringValue("dev")}
	diags := validateInstanceConfig(&model, declaredLists{Unknown: true}, "", "")
	if diags.ErrorsCount() != 0 {
		t.Errorf("errors = %d, want 0: %v", diags.ErrorsCount(), diags)
	}
	if diags.WarningsCount() != 0 {
		t.Errorf("warnings = %d, want 0: %v", diags.WarningsCount(), diags)
	}
}

// Lima ignores mounts and port forwarding entirely in plain mode, without
// complaint, and never starts the guest agent that implements forwarding. A
// configuration that declares both is almost certainly a mistake, and the only
// symptom is a service that cannot be reached.
func TestPlainModeWarnsAboutIgnoredSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     instanceModel
		lists     declaredLists
		wantWarns int
	}{
		{
			name: "plain with a port forward",
			model: instanceModel{
				Name:   types.StringValue("dev"),
				Config: types.StringValue("plain: true\n"),
			},
			lists: declaredLists{
				PortForwards:        []portModel{{GuestPort: types.Int64Value(80)}},
				PortForwardsManaged: true,
			},
			wantWarns: 1,
		},
		{
			name: "plain with a mount",
			model: instanceModel{
				Name:   types.StringValue("dev"),
				Config: types.StringValue("plain: true\n"),
			},
			lists: declaredLists{
				Mounts:        []mountModel{{Location: types.StringValue("/tmp/x")}},
				MountsManaged: true,
			},
			wantWarns: 1,
		},
		{
			name: "plain alone is fine",
			model: instanceModel{
				Name:   types.StringValue("dev"),
				Config: types.StringValue("plain: true\n"),
			},
		},
		{
			name: "forwards without plain are fine",
			model: instanceModel{
				Name:   types.StringValue("dev"),
				Config: types.StringValue("cpus: 2\n"),
			},
			lists: declaredLists{
				PortForwards:        []portModel{{GuestPort: types.Int64Value(80)}},
				PortForwardsManaged: true,
			},
		},
		{
			name: "plain set through config_overrides counts too",
			model: instanceModel{
				Name:            types.StringValue("dev"),
				Template:        types.StringValue("template:ubuntu"),
				ConfigOverrides: types.StringValue("plain: true\n"),
			},
			lists: declaredLists{
				PortForwards:        []portModel{{GuestPort: types.Int64Value(80)}},
				PortForwardsManaged: true,
			},
			wantWarns: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			diags := validateInstanceConfig(&tc.model, tc.lists, "", "")
			if diags.ErrorsCount() != 0 {
				t.Fatalf("unexpected errors: %v", diags)
			}
			if diags.WarningsCount() != tc.wantWarns {
				t.Errorf("warnings = %d, want %d: %v", diags.WarningsCount(), tc.wantWarns, diags)
			}
		})
	}
}
