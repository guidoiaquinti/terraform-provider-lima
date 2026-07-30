// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
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

// A refresh must only overwrite `start` when Lima reports a state that settles
// the question.
//
// Deriving it from `status == running` meant every other status read as "stopped":
// a half-created instance, a broken one, or a status a newer Lima introduces would
// all record start = false and manufacture a diff proposing a start that the user
// never asked for.
func TestObservedStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  string
		current types.Bool
		want    types.Bool
	}{
		{
			name:    "running settles it",
			status:  "Running",
			current: types.BoolValue(false),
			want:    types.BoolValue(true),
		},
		{
			name:    "stopped settles it",
			status:  "Stopped",
			current: types.BoolValue(true),
			want:    types.BoolValue(false),
		},
		{
			// Lima has registered the instance but not finished materialising it.
			name:    "creating leaves the desired state alone",
			status:  "Installing",
			current: types.BoolValue(true),
			want:    types.BoolValue(true),
		},
		{
			name:    "broken leaves the desired state alone",
			status:  "Broken",
			current: types.BoolValue(true),
			want:    types.BoolValue(true),
		},
		{
			// A status this provider version does not know must not be read as
			// "not running".
			name:    "unknown leaves the desired state alone",
			status:  "Hibernating",
			current: types.BoolValue(true),
			want:    types.BoolValue(true),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := observedStart(tc.current, lima.Instance{RawStatus: tc.status})
			if !got.Equal(tc.want) {
				t.Errorf("observedStart(%v, %q) = %v, want %v", tc.current, tc.status, got, tc.want)
			}
		})
	}
}
