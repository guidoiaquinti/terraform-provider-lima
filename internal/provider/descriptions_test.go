// Copyright 2026 Guido Iaquinti
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/guidoiaquinti/terraform-provider-lima/internal/lima"
)

// Every validator and plan modifier has to describe itself.
//
// These look like boilerplate, and they were the only completely uncovered part
// of the package, but the framework prints these strings: a validator's
// description is what a user sees when the framework explains *why* a value was
// rejected, and `terraform plan` renders a plan modifier's description when it
// forces a replacement. An empty one turns a clear failure into a bare
// "invalid value".
//
// Checking them by iteration rather than one test each is deliberate: the point
// is that no implementation can be added without a description, and a per-type
// test would simply be forgotten alongside the description itself.

func TestEveryValidatorDescribesItself(t *testing.T) {
	t.Parallel()

	validators := map[string]validator.String{
		"InstanceName": InstanceName(),
		"Size":         Size("memory"),
		"YAML":         YAML("config"),
		"KnownValue":   KnownValue("arch", lima.KnownArches),
		"OneOf":        OneOf("mode", []string{"system", "user"}),
		"AbsolutePath": AbsolutePath(),
		"NonEmpty":     NonEmpty("location"),
		"Duration":     Duration(),
	}

	ctx := context.Background()
	for name, v := range validators {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			plain := v.Description(ctx)
			if strings.TrimSpace(plain) == "" {
				t.Fatal("Description is empty; the framework prints it when a value is rejected")
			}
			markdown := v.MarkdownDescription(ctx)
			if strings.TrimSpace(markdown) == "" {
				t.Error("MarkdownDescription is empty")
			}
			// A description that merely repeats the type name tells a user
			// nothing. Every one of these should read as a requirement.
			if strings.EqualFold(plain, name) {
				t.Errorf("Description is just the validator name (%q)", plain)
			}
		})
	}
}

// The label a parameterised validator is constructed with reaches the user
// through the *diagnostic summary*, not through Description — the framework
// already reports which attribute failed, so repeating it in the description
// would be noise. This pins where the label actually shows up, because a
// validator that dropped it would produce a bare "Invalid " for every attribute.
func TestParameterisedValidatorsNameTheirAttributeInDiagnostics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	for _, tc := range []struct {
		label string
		value string
	}{
		{"memory", "not a size"},
		{"disk", "also not a size"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()

			req := validator.StringRequest{
				Path:        path.Root(tc.label),
				ConfigValue: types.StringValue(tc.value),
			}
			resp := &validator.StringResponse{}
			Size(tc.label).ValidateString(ctx, req, resp)

			diags := harnessDiags{resp.Diagnostics}
			diags.requireError(t, "Invalid "+tc.label)
		})
	}
}

// The two enumerating validators are deliberately different, and the difference
// is the provider's forward-compatibility policy rather than an inconsistency:
//
//	OneOf       closed set. Values are named in the description, because the set
//	            is fixed and a user can rely on it.
//	KnownValue  open set. An unrecognised value warns and is passed through, so a
//	            newer Lima backend or architecture works without a provider
//	            release. Its description must NOT read as a closed list, or the
//	            documentation would promise a restriction the code does not apply.
//
// Collapsing these two into one shape is the tempting cleanup that would quietly
// break that policy, so both halves are pinned.
func TestEnumeratingValidatorsMatchTheirOpenOrClosedPolicy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("OneOf names its closed set", func(t *testing.T) {
		t.Parallel()

		got := OneOf("mode", []string{"system", "user"}).Description(ctx)
		for _, want := range []string{"system", "user"} {
			if !strings.Contains(got, want) {
				t.Errorf("OneOf description %q does not list the allowed value %q", got, want)
			}
		}
		if !strings.Contains(got, "must") {
			t.Errorf("OneOf description %q does not read as a requirement", got)
		}
	})

	t.Run("KnownValue does not promise a closed set", func(t *testing.T) {
		t.Parallel()

		got := KnownValue("arch", []string{"x86_64", "aarch64"}).Description(ctx)
		if strings.Contains(got, "must") {
			t.Errorf("KnownValue description %q reads as a requirement, but an unrecognised value only warns", got)
		}
	})

	t.Run("KnownValue warns and lists what it knows", func(t *testing.T) {
		t.Parallel()

		resp := &validator.StringResponse{}
		KnownValue("arch", []string{"x86_64", "aarch64"}).ValidateString(ctx, validator.StringRequest{
			Path:        path.Root("arch"),
			ConfigValue: types.StringValue("riscv64"),
		}, resp)

		diags := harnessDiags{resp.Diagnostics}
		if diags.HasError() {
			t.Fatalf("an unrecognised arch was rejected rather than warned about:\n%s", diags.text())
		}
		if !diags.hasWarning("aarch64") {
			t.Errorf("the warning does not say which values the provider knows about; got:\n%s", diags.text())
		}
		if !diags.hasWarning("passed through to Lima") {
			t.Errorf("the warning does not say the value is still passed through; got:\n%s", diags.text())
		}
	})

	// An empty string is a different case from an unrecognised one: it cannot be
	// a future Lima value, so it is an error rather than a warning.
	t.Run("KnownValue rejects an empty string outright", func(t *testing.T) {
		t.Parallel()

		resp := &validator.StringResponse{}
		KnownValue("arch", []string{"x86_64"}).ValidateString(ctx, validator.StringRequest{
			Path:        path.Root("arch"),
			ConfigValue: types.StringValue(""),
		}, resp)

		harnessDiags{resp.Diagnostics}.requireError(t, "Empty arch")
	})
}

func TestEveryPlanModifierDescribesItself(t *testing.T) {
	t.Parallel()

	modifiers := map[string]planmodifier.String{
		"DiskGrowOnly":        DiskGrowOnly(),
		"ReplaceOnRealChange": ReplaceOnRealChange(),
	}

	ctx := context.Background()
	for name, m := range modifiers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			plain := m.Description(ctx)
			if strings.TrimSpace(plain) == "" {
				t.Fatal("Description is empty; terraform plan prints it when this modifier acts")
			}
			if strings.TrimSpace(m.MarkdownDescription(ctx)) == "" {
				t.Error("MarkdownDescription is empty")
			}
		})
	}
}

// ReplaceOnRealChange is the modifier whose behaviour is least obvious from its
// name — it forces replacement only between two known, differing values, which
// is what makes adopting an imported attribute and un-managing one both
// non-destructive. Its description has to convey that, because a user reading a
// plan needs to know why some changes replace and some do not.
func TestReplaceOnRealChangeDescribesTheKnownValueCondition(t *testing.T) {
	t.Parallel()

	got := strings.ToLower(ReplaceOnRealChange().Description(context.Background()))
	for _, want := range []string{"replace", "known"} {
		if !strings.Contains(got, want) {
			t.Errorf("description %q does not mention %q, so a reader cannot tell why an import does not replace",
				got, want)
		}
	}
}
